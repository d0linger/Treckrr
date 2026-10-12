FROM mcr.microsoft.com/playwright:v1.64.0-noble@sha256:06a9939e57531807f8d5fd76ce44b53165ffb7d7501d87ab10e285c20b1e971f
WORKDIR /work/e2e
COPY e2e/package.json e2e/package-lock.json ./
RUN npm ci --ignore-scripts
COPY e2e/playwright.config.ts ./
COPY e2e/tests ./tests
COPY internal/web/static /work/internal/web/static
COPY internal/web/templates /work/internal/web/templates
CMD ["npx", "playwright", "test"]
