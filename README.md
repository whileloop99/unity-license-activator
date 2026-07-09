# Unity License Activator

Automated Unity personal license activation via [playwright-go](https://github.com/mxschmitt/playwright-go) (headless Chromium).

## Usage

```sh
GOPROXY=direct go run github.com/whileloop99/unity-license-activator@latest \
  --email=<your-email> \
  --password=<your-password> \
  --alf=<path-to.alf> \
  --totp=<your-base32-totp-secret>
```

### Docker

```sh
docker run --rm -v ./Unity_v2022.3.31f1.alf:/data/license.alf:ro \
  ghcr.io/whileloop99/unity-license-activator:latest \
  --email=<your-email> \
  --password=<your-password> \
  --alf=/data/license.alf \
  --totp=<your-base32-totp-secret>
```

## One-liner

```sh
GOPROXY=direct go run github.com/whileloop99/unity-license-activator@latest --email=user@example.com --password=secret --alf=./Unity_v2022.3.31f1.alf --totp=JBSWY3DPEHPK3PXP
```

## Flags

| Flag            | Required | Default     | Description                                         |
|-----------------|----------|-------------|-----------------------------------------------------|
| `--email`       | yes      |             | Unity ID email                                      |
| `--password`    | yes      |             | Unity ID password                                   |
| `--alf`         | yes      |             | Path to `.alf` license request file                 |
| `--totp`        | if 2FA   |             | Base32 TOTP secret                                  |
| `--ulf-name`    | no       | `unity.ulf` | ULF output filename                                 |
| `--output`      | no       | `.output`   | Output directory for ULF, screenshots, error dumps  |
| `--data-dir`    | no       |             | Persistent Chrome profile (reuses session)          |
| `--skip-install`| no       | `false`     | Skip browser download (pre-installed browsers)      |

## Examples

**First run** (login + 2FA + download, ~34s):

```sh
GOPROXY=direct go run github.com/whileloop99/unity-license-activator@latest \
  --email=user@example.com \
  --password=secret \
  --alf=./Unity_v2022.3.31f1.alf \
  --totp=JBSWY3DPEHPK3PXP
```

**Subsequent runs** (session reuse, ~18s):

```sh
GOPROXY=direct go run github.com/whileloop99/unity-license-activator@latest \
  --email=user@example.com \
  --password=secret \
  --alf=./Unity_v2022.3.31f1.alf \
  --totp=JBSWY3DPEHPK3PXP \
  --data-dir=.chrome-data
```

**Docker Compose** (no Go needed):

```sh
docker compose run --rm activator \
  --email=user@example.com \
  --password=secret \
  --alf=/data/Unity_v2022.3.31f1.alf \
  --totp=JBSWY3DPEHPK3PXP
```

## Output

All generated files go to `--output` directory (default `.output`):

| File             | Content                                            |
|------------------|----------------------------------------------------|
| `{ulf-name}`     | Downloaded license file (default: `unity.ulf`)     |
| `error.png`      | Screenshot on crash                                |
| `error.html`     | Page HTML on crash                                 |

## How it works

1. Navigate to `license.unity3d.com/manual` — redirects to login with OAuth state
2. Fill email → Continue → Fill password → Sign in
3. If TFA prompted: compute TOTP (RFC 6238), fill, submit
4. Navigate to license page with active session
5. Accept ToS if prompted
6. Upload `.alf` file → server generates `.ulf`
7. If session expired after upload: re-login and retry automatically
8. Select personal license → submit → download `.ulf`
9. Save `.ulf` to output directory

With `--data-dir`, the Chrome user profile persists between runs. Session cookies
carry over, skipping steps 1–4 on subsequent runs (~18s vs ~34s).

## Requirements

- **Go 1.21+** (for local `go run`)
- **Or Docker** (no Go toolchain needed)

Playwright browsers are installed automatically on first run, or pre-installed
in the Docker image.
