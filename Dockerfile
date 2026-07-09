# ── Unity License Activator ──────────────────────────────────────────────────
# Build + download Chrome → collect libs → distroless runtime.

# ── Stage 1: Build ───────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS build

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags='-s -w' -o /activator .

# Download playwright's bundled Chromium
ENV PLAYWRIGHT_BROWSERS_PATH=/browsers
RUN mkdir -p /browsers && \
    printf '%s\n' \
    'package main' \
    'import ("os"; "github.com/mxschmitt/playwright-go")' \
    'func main() {' \
    '  os.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/browsers")' \
    '  if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {' \
    '    panic(err)' \
    '  }' \
    '}' > /app/install_browsers.go && \
    go run /app/install_browsers.go && \
    rm /app/install_browsers.go

# Strip Chrome binary (path varies by playwright version)
RUN apt-get update && apt-get install -y binutils --no-install-recommends && \
    chrome_bin=$(find /browsers -name chrome -type f | head -1) && \
    strip "$chrome_bin" && \
    rm -rf /var/lib/apt/lists/*

# ── Stage 2: Collect dependencies ────────────────────────────────────────────
FROM debian:bookworm-slim AS libs

RUN apt-get update && apt-get install -y \
    ca-certificates libnss3 libnspr4 \
    libatk1.0-0 libatk-bridge2.0-0 \
    libcups2 libdrm2 libdbus-1-3 \
    libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 \
    libgbm1 libpango-1.0-0 libcairo2 libasound2 \
    --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*

# Collect all .so files + fonts + certs into /collect
RUN mkdir -p /collect/lib /collect/etc /collect/usr/share && \
    cp -r /etc/ssl /collect/etc/ && \
    { cp -r /etc/fonts /collect/etc/ 2>/dev/null || true; } && \
    { cp -r /usr/share/fonts /collect/usr/share/ 2>/dev/null || true; } && \
    { cp -r /usr/share/fontconfig /collect/usr/share/ 2>/dev/null || true; } && \
    { cp -d /lib/x86_64-linux-gnu/*.so* /collect/lib/ 2>/dev/null || true; } && \
    { cp -d /usr/lib/x86_64-linux-gnu/*.so* /collect/lib/ 2>/dev/null || true; } && \
    ldconfig -n /collect/lib 2>/dev/null || true

# ── Stage 3: Runtime (distroless) ────────────────────────────────────────────
FROM gcr.io/distroless/base-debian12

COPY --from=build /activator /usr/local/bin/activator
COPY --from=build /browsers /browsers
COPY --from=build /root/.cache/ms-playwright-go /root/.cache/ms-playwright-go
COPY --from=libs /collect/lib /lib
COPY --from=libs /collect/etc /etc
COPY --from=libs /collect/usr/share /usr/share

ENV PLAYWRIGHT_BROWSERS_PATH=/browsers
WORKDIR /data
ENTRYPOINT ["activator", "--skip-install"]
