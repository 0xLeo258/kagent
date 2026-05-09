### STAGE 1: Python base with UV
FROM ghcr.io/astral-sh/uv:python3.13-trixie-slim

WORKDIR /app

# Install system dependencies + Codex CLI
RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Codex CLI binary (linux x86_64 musl - static linked)
# Update the URL to the latest release when available
ARG CODEX_VERSION=latest
RUN ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "amd64" ]; then \
        CODEX_ARCH="x86_64-unknown-linux-musl"; \
    elif [ "$ARCH" = "arm64" ]; then \
        CODEX_ARCH="aarch64-unknown-linux-musl"; \
    else \
        echo "Unsupported architecture: $ARCH" && exit 1; \
    fi && \
    curl -fsSL "https://github.com/openai/codex/releases/latest/download/codex-${CODEX_ARCH}.tar.gz" \
        -o /tmp/codex.tar.gz && \
    tar -xzf /tmp/codex.tar.gz -C /tmp/ && \
    mv /tmp/codex-${CODEX_ARCH} /usr/local/bin/codex && \
    chmod +x /usr/local/bin/codex && \
    rm /tmp/codex.tar.gz

# Copy Python workspace files
COPY pyproject.toml pyproject.toml
COPY packages packages
COPY samples/sota/codex_agent/ ./samples/sota/codex_agent/
COPY README.md README.md
COPY .python-version .python-version
COPY uv.lock uv.lock

# Install dependencies
RUN uv venv && uv sync --no-dev --package codex-agent-sample

# Environment
ENV PORT=8080
ENV VIRTUAL_ENV=/app/.venv
ENV PATH="/app/.venv/bin:$PATH"
ENV CODEX_UNSAFE_ALLOW_NO_SANDBOX=1

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8080/health || exit 1

CMD ["python", "samples/sota/codex_agent/codex_agent/agent.py"]
