FROM mcr.microsoft.com/playwright:v1.64.1-noble@sha256:47b510dd9230bf0b9bfc9b20102241e577ea7a99334ab4c3ca19f19770e9b8ea
WORKDIR /work/e2e
COPY e2e/package.json e2e/package-lock.json ./
RUN npm ci --ignore-scripts
COPY e2e/playwright.config.ts ./
COPY e2e/tests ./tests
COPY internal/web/static /work/internal/web/static
COPY internal/web/templates /work/internal/web/templates
CMD ["npx", "playwright", "test"]
