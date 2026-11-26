module github.com/OrdalieTech/orb/platforms/desktop

go 1.27.3

require (
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/cloudflare/circl v1.6.2 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	go.digitalxero.dev/rpm v0.3.4 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

require (
	github.com/OrdalieTech/orb v0.0.0
	github.com/egoist/mygo v0.10.0
	github.com/go-text/typesetting v0.3.5
)

// Only Orb's QR encoder is linked from it.
replace github.com/OrdalieTech/orb => ../..

tool github.com/egoist/mygo/cmd/mygo
