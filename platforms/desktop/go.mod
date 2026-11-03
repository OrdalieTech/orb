module github.com/OrdalieTech/orb/platforms/desktop

go 1.27.3

require (
	github.com/ebitengine/purego v0.11.1 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

require (
	github.com/OrdalieTech/orb v0.0.0
	github.com/egoist/mygo v0.7.4
	github.com/go-text/typesetting v0.3.5
)

// Only Orb's QR encoder is linked from it.
replace github.com/OrdalieTech/orb => ../..

tool github.com/egoist/mygo/cmd/mygo
