FROM debian:bookworm-slim

# Virtual X11 display, screenshot capture, and example graphical applications.
RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb x11-utils ffmpeg x11-apps chromium fonts-dejavu-core ca-certificates \
    && rm -rf /var/lib/apt/lists/*
