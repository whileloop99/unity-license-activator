# Unity License Activation

Automated Unity personal license activation via chromedp (headless Chrome).

## Usage

```sh
go run . [flags]
```

## Flags


| Flag         | Required | Default     | Description                                             |
| ------------ | -------- | ----------- | ------------------------------------------------------- |
| `--email`    | yes      |             | Unity ID email                                          |
| `--password` | yes      |             | Unity ID password                                       |
| `--alf`      | yes      |             | Path to .alf license request file                       |
| `--totp`     | if 2FA   |             | Base32 TOTP secret                                      |
| `--ulf-name` | no       | `unity.ulf` | ULF output filename                                     |
| `--output`   | no       | `.output`   | Output directory for ULF, screenshots, error dumps      |
| `--data-dir` | no       |             | Persistent Chrome profile (reuses session, skips login) |




## Examples

First run (login + 2FA + download):

```sh
go run . --email=user@example.com \
         --password=secret \
         --alf=./Unity_v2022.3.31f1.alf \
         --totp=JBSWY3DPEHPK3PXP
```

Subsequent runs (session reuse, ~18s):

```sh
go run . --email=user@example.com 
         --password=secret \
         --alf=./Unity_v2022.3.31f1.alf \
         --totp=JBSWY3DPEHPK3PXP \
         --data-dir=.chrome-data
```



## Output

All generated files go to `--output` directory (default `.output`):


| File         | Content                                        |
| ------------ | ---------------------------------------------- |
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

- Go 1.21+
- Chrome / Chromium (chromedp manages headless instance)

