// file: cmd/fp_worker.go
// version: 1.1.0
// guid: 588cd98f-d5c3-478a-825f-809163a8edd3
// last-edited: 2026-09-19

package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerclient"
)

var (
	fpwServer      string
	fpwWorkerID    string
	fpwRoots       []string
	fpwConcurrency int
	fpwMaxJobs     int
	fpwKeyFile     string
	fpwFpcalc      string
	fpwFFmpeg      string
	fpwCAFile      string
	fpwFSTypes     []string
	fpwTimeout     time.Duration
)

var fpWorkerCmd = &cobra.Command{
	Use:   "fp-worker",
	Short: "Cut windowed fingerprints for the server from a read-only library mount",
	Long: `fp-worker leases windowed-fingerprint jobs from a server's remote worker API
(/api/v1/fingerprint/worker, enabled by fingerprint_remote_workers_enabled while an
acoustid.window-backfill runs), cuts each window with ffmpeg | fpcalc from this
host's READ-ONLY mount of the library, and posts the prints back.

Before leasing anything it checks, and exits non-zero on any failure, that:
  - every --root is a read-only network mount (statfs, then a write probe);
  - this host's fpcalc/ffmpeg pair and pipeline are allowlisted by the server;
  - the server's calibration files resolve to the same bytes here, and cutting
    their windows here reproduces the server's stored prints byte for byte.

The API key is read from --key-file (mode 0600, the key alone on one line) or from
the ` + workerclient.KeyEnvVar + ` environment variable; it is never a flag value.

SIGINT/SIGTERM drains: in-flight jobs finish, their results are posted, jobs not
started are released back to the server, and the process exits 0. A second
signal stops at once (unfinished leases then expire on the server).

Example:
  audiobook-organizer fp-worker --server https://library.example:8484 \
    --key-file ~/.config/fp-worker/key --root libroot=/Volumes/library/audiobooks`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	// fp-worker runs on another host in whatever directory it is started
	// from: none of the app setup in initConfig applies to it.
	Annotations: map[string]string{skipAppInitAnnotation: "true"},
	RunE:        runFPWorker,
}

func init() {
	rootCmd.AddCommand(fpWorkerCmd)
	f := fpWorkerCmd.Flags()
	f.StringVar(&fpwServer, "server", "", "server base URL (https://host:port)")
	f.StringVar(&fpwWorkerID, "worker-id", "", "worker ID reported to the server (default: hostname)")
	f.StringArrayVar(&fpwRoots, "root", nil, "server root name = local read-only mount, e.g. libroot=/Volumes/library (repeatable; default from "+workerclient.RootsEnvVar+")")
	f.IntVar(&fpwConcurrency, "concurrency", workerclient.DefaultConcurrency, "jobs cut at once")
	f.IntVar(&fpwMaxJobs, "max-jobs", 0, "jobs asked for per lease (0 = the server's maximum)")
	f.StringVar(&fpwKeyFile, "key-file", "", "file holding the API key (mode 0600); default: the "+workerclient.KeyEnvVar+" environment variable")
	f.StringVar(&fpwFpcalc, "fpcalc", "fpcalc", "fpcalc binary (name on PATH or a path)")
	f.StringVar(&fpwFFmpeg, "ffmpeg", "ffmpeg", "ffmpeg binary (name on PATH or a path)")
	f.StringVar(&fpwCAFile, "ca-file", "", "PEM file of CA certificates to trust for --server (e.g. the server's self-signed certificate)")
	f.StringSliceVar(&fpwFSTypes, "allow-fstype", []string{"nfs"}, "filesystem types a --root may live on")
	f.DurationVar(&fpwTimeout, "window-timeout", fingerprint.DefaultWindowTimeout, "timeout for cutting one window")
}

func runFPWorker(cmd *cobra.Command, _ []string) error {
	if fpwServer == "" {
		return errors.New("fp-worker: --server is required")
	}
	key, err := fpWorkerAPIKey(fpwKeyFile)
	if err != nil {
		return err
	}
	rootEntries := fpwRoots
	if len(rootEntries) == 0 {
		rootEntries = workerclient.SplitRootsEnv(os.Getenv(workerclient.RootsEnvVar))
	}
	roots, err := workerclient.ParseRoots(rootEntries)
	if err != nil {
		return err
	}
	workerID := fpwWorkerID
	if workerID == "" {
		workerID = workerclient.DefaultWorkerID()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fpPath, err := exec.LookPath(fpwFpcalc)
	if err != nil {
		return fmt.Errorf("fp-worker: fpcalc: %w", err)
	}
	ffPath, err := exec.LookPath(fpwFFmpeg)
	if err != nil {
		return fmt.Errorf("fp-worker: ffmpeg: %w", err)
	}
	versions, err := fingerprint.ToolVersions(ctx, fpPath, ffPath)
	if err != nil {
		return fmt.Errorf("fp-worker: %w", err)
	}
	tools := fingerprint.WindowTools{FpcalcPath: fpPath, FFmpegPath: ffPath, Versions: versions, Timeout: fpwTimeout}

	hc, err := fpWorkerHTTPClient(fpwCAFile)
	if err != nil {
		return err
	}

	drain := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go fpWorkerSignals(sigs, drain, done, cancel, fpWorkerHardStopGrace, os.Exit, cmd.ErrOrStderr())

	return workerclient.Run(ctx, drain, workerclient.Config{
		ServerURL:      fpwServer,
		APIKey:         key,
		WorkerID:       workerID,
		Roots:          roots,
		Concurrency:    fpwConcurrency,
		MaxJobs:        fpwMaxJobs,
		Versions:       versions,
		Cut:            tools.FileWindow,
		HTTPClient:     hc,
		AllowedFSTypes: fpwFSTypes,
	})
}

// fpWorkerHardStopGrace is how long a second signal waits for Run to return
// before the process exits regardless.
const fpWorkerHardStopGrace = 10 * time.Second

// fpWorkerAPIKey loads the key and removes it from this process's
// environment, so no child process (ffmpeg, fpcalc) can inherit it.
func fpWorkerAPIKey(keyFile string) (string, error) {
	k, err := workerclient.LoadAPIKey(keyFile, os.Getenv)
	_ = os.Unsetenv(workerclient.KeyEnvVar)
	return k, err
}

// fpWorkerSignals handles SIGINT/SIGTERM. The first closes drain (graceful:
// in-flight jobs finish and post, the rest are released). The second
// cancels the hard context and, since a hung hard NFS mount can leave
// goroutines stuck in Lstat/Open or ffmpeg in uninterruptible sleep where no
// cancellation reaches them, calls exit(130) after grace if Run has not
// returned by then. done is closed when Run returns.
func fpWorkerSignals(sigs <-chan os.Signal, drain chan<- struct{}, done <-chan struct{}, cancel func(),
	grace time.Duration, exit func(int), stderr io.Writer) {
	select {
	case <-sigs:
	case <-done:
		return
	}
	close(drain)
	select {
	case <-sigs:
	case <-done:
		return
	}
	fmt.Fprintf(stderr, "fp-worker: second signal: stopping now (forced exit in %s if jobs are stuck)\n", grace)
	cancel()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-t.C:
		fmt.Fprintln(stderr, "fp-worker: jobs did not stop in time; exiting (their leases expire on the server)")
		exit(130)
	case <-done:
	}
}

// fpWorkerHTTPClient trusts the system roots plus caFile, if given.
func fpWorkerHTTPClient(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("fp-worker: --ca-file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("fp-worker: --ca-file %s holds no PEM certificate", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   2 * time.Minute,
		// Never follow a redirect: the request carries the bearer key, and
		// the worker API never redirects. The 3xx comes back as the answer.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
