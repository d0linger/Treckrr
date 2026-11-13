module github.com/d0linger/treckrr

// The LANGUAGE floor, not the toolchain. Keep it at the oldest Go the code
// actually needs: prebuilt analyzers (CodeQL's autobuilder, gosec, golangci-lint)
// ship their own Go with GOTOOLCHAIN=local and cannot download a newer one, so
// raising this floor breaks them for no benefit. The toolchain that builds,
// tests and ships is pinned separately (Dockerfile, Makefile, CI GO_VERSION).
go 1.26.6

// Prefer the patched application toolchain for automatic local builds.
// Explicit analyzer compatibility pins still override this preference.
toolchain go1.27.4

require (
	github.com/go-webauthn/webauthn v0.18.2
	github.com/jackc/pgx/v5 v5.13.0
	github.com/minio/minio-go/v7 v7.4.1
	github.com/robfig/cron/v3 v3.0.1
	github.com/shopspring/decimal v1.6.0
	github.com/signintech/gopdf v0.38.4
	golang.org/x/crypto v0.59.0
	golang.org/x/image v0.48.0
	golang.org/x/net v0.62.0
	rsc.io/qr v0.2.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/fxamacker/cbor/v2 v2.10.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/philhofer/fwd v1.2.1 // indirect
	github.com/phpdave11/gofpdi v1.0.16 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/tinylib/msgp v1.6.5 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	gopkg.in/ini.v1 v1.68.0 // indirect
)
