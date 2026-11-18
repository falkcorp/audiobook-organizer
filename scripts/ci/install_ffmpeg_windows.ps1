# file: scripts/ci/install_ffmpeg_windows.ps1
# version: 1.0.0
# guid: B253287E-60CF-436F-9565-39DEB24A344C
# last-edited: 2026-11-18
#
# Bounded, retrying ffmpeg + ffprobe install for the Windows CI runner.
#
# 1. Already on PATH (both tools): nothing to do.
# 2. Chocolatey, up to 3 attempts, each killed after 150 s, with backoff. The
#    community feed answers HTTP errors and hangs now and then; a bare
#    `choco install` failed a merge-queue run on one such answer.
# 3. Fallback: the pinned GyanD/codexffmpeg release zip from GitHub Releases,
#    downloaded with retries and verified against a pinned SHA-256 before it is
#    unpacked. The hash is the one GitHub itself reports for the asset
#    (`gh api repos/GyanD/codexffmpeg/releases/tags/9.0.2`). To bump the pin,
#    change $Version and $Sha256 together.
#
# The script exits non-zero if neither path leaves a working ffmpeg and
# ffprobe, so the step fails here with the reason instead of failing later in
# test_ci_has_ffmpeg_when_it_says_it_does.
$ErrorActionPreference = 'Stop'

$Version = '9.0.2'
$Sha256 = '60f467265b1e312373dbcd92200c2618a74850f98d3d078e94296bb3fa2047ba'
$Url = "https://github.com/GyanD/codexffmpeg/releases/download/$Version/ffmpeg-$Version-essentials_build.zip"

function Test-Ffmpeg {
    return ($null -ne (Get-Command ffmpeg -ErrorAction SilentlyContinue)) -and
           ($null -ne (Get-Command ffprobe -ErrorAction SilentlyContinue))
}

function Invoke-Choco {
    $p = Start-Process -FilePath choco -ArgumentList 'install', 'ffmpeg', '-y', '--no-progress' `
        -NoNewWindow -PassThru
    if (-not $p.WaitForExit(150000)) {
        Write-Host 'choco did not finish within 150 s; killing it'
        & taskkill /T /F /PID $p.Id | Out-Null
        return $false
    }
    # 0 = ok; 1641 / 3010 = ok, reboot requested.
    return @(0, 1641, 3010) -contains $p.ExitCode
}

function Install-FromChocolatey {
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        Write-Host "choco install ffmpeg: attempt $attempt/3"
        try { $null = Invoke-Choco } catch { Write-Host "choco error: $_" }
        if (Test-Ffmpeg) { return $true }
        if ($attempt -lt 3) { Start-Sleep -Seconds (15 * $attempt) }
    }
    return $false
}

function Install-FromPinnedZip {
    $dir = Join-Path $env:RUNNER_TEMP "ffmpeg-$Version"
    $zip = "$dir.zip"
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        Write-Host "download ffmpeg $Version zip: attempt $attempt/3"
        try {
            Remove-Item -Force -ErrorAction SilentlyContinue $zip
            $ProgressPreference = 'SilentlyContinue'
            Invoke-WebRequest -Uri $Url -OutFile $zip -TimeoutSec 120 -UseBasicParsing
            $got = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLowerInvariant()
            if ($got -ne $Sha256) {
                throw "SHA-256 mismatch for ${Url}: got $got, want $Sha256"
            }
            Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $dir
            Expand-Archive -Path $zip -DestinationPath $dir -Force
            $bin = Join-Path $dir "ffmpeg-$Version-essentials_build\bin"
            if (-not (Test-Path (Join-Path $bin 'ffmpeg.exe'))) { throw "no ffmpeg.exe under $bin" }
            $env:PATH = "$bin;$env:PATH"
            if ($env:GITHUB_PATH) { Add-Content -Path $env:GITHUB_PATH -Value $bin }
            return (Test-Ffmpeg)
        } catch {
            Write-Host "pinned download failed: $_"
            if ($attempt -lt 3) { Start-Sleep -Seconds (15 * $attempt) }
        }
    }
    return $false
}

if (-not (Test-Ffmpeg)) {
    if (-not (Install-FromChocolatey)) {
        Write-Host 'Chocolatey did not give a working ffmpeg; falling back to the pinned release zip'
        if (-not (Install-FromPinnedZip)) {
            Write-Error 'ffmpeg and ffprobe are still not on PATH after Chocolatey and the pinned download'
            exit 1
        }
    }
}
ffmpeg -version | Select-Object -First 1
ffprobe -version | Select-Object -First 1
