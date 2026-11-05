### Fixed

- CI: the Frontend Build and Test workflow's Go CI job now frees runner disk before restoring the Go cache. It had run out of space linking `-race` test binaries. The reusable workflow pin moves to falkcorp/github-common@8e4eeeb, which adds the `free-runner-disk` input and also picks up the helpers fix that makes a failed frontend npm script fail the job.
