FROM mcr.microsoft.com/playwright:v1.65.0-noble@sha256:6e3fb709361521eb27161606e6da6d2674e5e89fa16516cb37d6fdd91372273d
WORKDIR /work/e2e
COPY e2e/package.json e2e/package-lock.json ./
RUN npm ci --ignore-scripts
COPY e2e/playwright.config.ts ./
COPY e2e/tests ./tests
COPY internal/web/static /work/internal/web/static
COPY internal/web/templates /work/internal/web/templates
CMD ["npx", "playwright", "test"]
