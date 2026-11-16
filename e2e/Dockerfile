FROM mcr.microsoft.com/playwright:v1.65.1-noble@sha256:1507cbfc550a63a6b7396791ca4f00df17beb56342d2447b3f106c306f525579
WORKDIR /work/e2e
COPY e2e/package.json e2e/package-lock.json ./
RUN npm ci --ignore-scripts
COPY e2e/playwright.config.ts ./
COPY e2e/tests ./tests
COPY internal/web/static /work/internal/web/static
COPY internal/web/templates /work/internal/web/templates
CMD ["npx", "playwright", "test"]
