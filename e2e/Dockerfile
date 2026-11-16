FROM mcr.microsoft.com/playwright:v1.65.0-noble@sha256:9beb546099e11f52d62e505537914870e01a67e0e6b00ae41da4775324e73dbe
WORKDIR /work/e2e
COPY e2e/package.json e2e/package-lock.json ./
RUN npm ci --ignore-scripts
COPY e2e/playwright.config.ts ./
COPY e2e/tests ./tests
COPY internal/web/static /work/internal/web/static
COPY internal/web/templates /work/internal/web/templates
CMD ["npx", "playwright", "test"]
