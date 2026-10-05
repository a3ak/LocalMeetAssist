module localmeetassist

go 1.24.0

require (
	github.com/getlantern/systray v1.2.2
	github.com/hajimehoshi/go-mp3 v0.3.4
	github.com/pion/opus v0.1.1-0.20260923000601-86ced7318333
	github.com/yalue/onnxruntime_go v1.23.0
	go.etcd.io/bbolt v1.3.8
	golang.design/x/hotkey v0.0.0
	gonum.org/v1/gonum v0.16.0
)

require golang.org/x/sys v0.17.0 // indirect

replace go.etcd.io/bbolt => ./third_party/go.etcd.io/bbolt

replace golang.org/x/sys => ./third_party/golang.org/x/sys

replace github.com/pion/opus => ./third_party/github.com/pion/opus

replace github.com/getlantern/systray => ./third_party/github.com/getlantern/systray

replace golang.design/x/hotkey => ./third_party/golang.design/x/hotkey
