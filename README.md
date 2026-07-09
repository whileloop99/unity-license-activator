# Unity License Activator

Automated Unity personal license activation via [playwright-go](https://github.com/mxschmitt/playwright-go) (headless Chromium).

## Quick start

```sh
go run . --email=user@example.com \
         --password=secret \
         --alf=./Unity_v2022.3.31f1.alf \
         --totp=JBSWY3DPEHPK3PXP
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
go run . --email=user@example.com \
         --password=secret \
         --alf=./Unity_v2022.3.31f1.alf \
         --totp=JBSWY3DPEHPK3PXP
```

**Subsequent runs** (session reuse, ~18s):

```sh
go run . --email=user@example.com \
         --password=secret \
         --alf=./Unity_v2022.3.31f1.alf \
         --totp=JBSWY3DPEHPK3PXP \
         --data-dir=.chrome-data
```

## Docker

Build and run inside a container — no Go toolchain required.

```sh
docker compose build
docker compose run --rm activator \
  --email=user@example.com \
  --password=secret \
  --alf=/data/Unity_v2022.3.31f1.alf \
  --totp=JBSWY3DPEHPK3PXP
```

Or use a `.env` file:

```sh
# .env
EMAIL=user@example.com
PASSWORD=secret
TOTP=JBSWY3DPEHPK3PXP
ALF_FILE=Unity_v2022.3.31f1.alf
```

Then:

```sh
docker compose run --rm activator
```

The Docker image includes all dependencies (Chromium, playwright driver, system libraries).
The `--skip-install` flag is enabled by default in the container entrypoint.

## Output

All generated files go to `--output` directory (default `.output`):

| File         | Content                                        |
|--------------|------------------------------------------------|
| `{ulf-name}` | Downloaded license file (default: `unity.ulf`) |
| `state.png`  | Screenshot on failure                          |
| `error.png`  | Screenshot on crash                            |
| `error.html` | Page HTML on crash                             |

## How it works

1. Navigate to `license.unity3d.com/manual` — redirects to login with OAuth state
2. Fill email → Continue → Fill password → Sign in
3. If TFA prompted: compute TOTP (RFC 6238), fill, submit
4. Navigate to license page with active session
5. Upload `.alf` file → server generates `.ulf`
6. Select personal license → submit → download `.ulf`
7. Save `.ulf` to output directory

With `--data-dir`, the Chrome user profile persists between runs. Session cookies
carry over, skipping steps 1–4 on subsequent runs (~18s vs ~34s).

## Requirements

- **Go 1.21+** (for local `go run`)
- **Or Docker** (no Go toolchain needed)

Playwright browsers are installed automatically on first run, or pre-installed
in the Docker image.
