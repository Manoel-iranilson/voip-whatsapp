package main

import (
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// makeBrowserOffer simulates the browser: it opens the "pcm" data channel and
// returns the SDP offer. It also returns the peer connection so a test can
// finish the handshake and exchange PCM.
func makeBrowserOffer(t *testing.T) (*webrtc.PeerConnection, *webrtc.DataChannel, string) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}

	dc, err := pc.CreateDataChannel(pcmChannelLabel, nil)
	if err != nil {
		t.Fatal(err)
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gather
	return pc, dc, pc.LocalDescription().SDP
}

func defaultBrowserAPI(t *testing.T) *webrtc.API {
	t.Helper()
	api, err := newBrowserAPI(iceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return api
}

// This is the difference between audio working and not working behind NAT.
//
// Without a public IP, pion advertises the addresses of the interfaces it can
// see. In a container on a bridge network those are private, the browser cannot
// reach them, and the call connects everywhere except the audio.
func TestBridgeAdvertisesThePublicIP(t *testing.T) {
	pc, _, offer := makeBrowserOffer(t)
	defer pc.Close()

	const publicIP = "203.0.113.42" // TEST-NET-3, cannot belong to this machine
	api, err := newBrowserAPI(iceConfig{PublicIP: publicIP})
	if err != nil {
		t.Fatal(err)
	}
	br, answer, err := NewBridge(api, offer, BridgeCallbacks{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()

	if !strings.Contains(answer, publicIP) {
		t.Fatalf("the answer never offers %s, so a browser behind NAT could not reach us:\n%s", publicIP, answer)
	}
	for _, line := range strings.Split(answer, "\n") {
		if !strings.Contains(line, "a=candidate") || !strings.Contains(line, "typ host") {
			continue
		}
		if !strings.Contains(line, publicIP) {
			t.Fatalf("a host candidate still leaks a local address: %s", strings.TrimSpace(line))
		}
	}
}

func TestBridgeHonoursTheUDPPortRange(t *testing.T) {
	pc, _, offer := makeBrowserOffer(t)
	defer pc.Close()

	const lo, hi = 49500, 49520
	api, err := newBrowserAPI(iceConfig{PublicIP: "203.0.113.42", UDPPortMin: lo, UDPPortMax: hi})
	if err != nil {
		t.Fatal(err)
	}
	br, answer, err := NewBridge(api, offer, BridgeCallbacks{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()

	var checked int
	for _, line := range strings.Split(answer, "\n") {
		if !strings.Contains(line, "a=candidate") {
			continue
		}
		// a=candidate:<foundation> <component> udp <priority> <ip> <port> typ ...
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 6 {
			continue
		}
		port, err := strconv.Atoi(fields[5])
		if err != nil {
			continue
		}
		checked++
		if port < lo || port > hi {
			t.Fatalf("candidate port %d is outside the published range %d-%d; the firewall would drop it", port, lo, hi)
		}
	}
	if checked == 0 {
		t.Fatal("the answer carried no candidates to check")
	}
}

// Left unset, nothing changes for anyone running this locally.
func TestBridgeWithoutICEConfigUsesLocalAddresses(t *testing.T) {
	pc, _, offer := makeBrowserOffer(t)
	defer pc.Close()

	br, answer, err := NewBridge(defaultBrowserAPI(t), offer, BridgeCallbacks{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()

	if !strings.Contains(answer, "a=candidate") {
		t.Fatalf("expected local candidates in the answer:\n%s", answer)
	}
	if strings.Contains(answer, "203.0.113.42") {
		t.Fatal("an unconfigured bridge must not invent an address")
	}
}

// A bad range has to stop the server at startup, not surface when an operator
// first tries to talk.
func TestICEConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  iceConfig
		ok   bool
	}{
		{"empty is fine", iceConfig{}, true},
		{"public ip only", iceConfig{PublicIP: "203.0.113.42"}, true},
		{"full range", iceConfig{PublicIP: "203.0.113.42", UDPPortMin: 50000, UDPPortMax: 50100}, true},
		{"not an ip", iceConfig{PublicIP: "not-an-ip"}, false},
		{"hostname is not an ip", iceConfig{PublicIP: "wacalls.example.com"}, false},
		{"inverted range", iceConfig{UDPPortMin: 50100, UDPPortMax: 50000}, false},
		{"half a range", iceConfig{UDPPortMin: 50000}, false},
		{"out of range", iceConfig{UDPPortMin: 50000, UDPPortMax: 70000}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newBrowserAPI(tc.cfg)
			if tc.ok && err != nil {
				t.Fatalf("should have been accepted: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("should have been rejected")
			}
		})
	}
}

func TestNewBridgeNegotiatesDataChannelOffer(t *testing.T) {
	pc, _, offer := makeBrowserOffer(t)
	defer pc.Close()

	br, answer, err := NewBridge(defaultBrowserAPI(t), offer, BridgeCallbacks{}, slog.Default())
	if err != nil {
		t.Fatalf("NewBridge failed: %v", err)
	}
	defer br.Close()

	if answer == "" || !strings.Contains(answer, "m=application") {
		t.Fatalf("answer missing data channel (application) m-line:\n%s", answer)
	}
	if strings.Contains(answer, "m=audio") {
		t.Fatalf("answer should not negotiate an audio m-line anymore:\n%s", answer)
	}
}

// TestBridgePCMRoundtrip connects the simulated browser to the bridge and checks
// that PCM sent on the data channel surfaces as float32 via OnBrowserPCM.
func TestBridgePCMRoundtrip(t *testing.T) {
	pc, dc, offer := makeBrowserOffer(t)
	defer pc.Close()

	got := make(chan []float32, 1)
	cb := BridgeCallbacks{OnBrowserPCM: func(pcm []float32) { got <- pcm }}
	br, answer, err := NewBridge(defaultBrowserAPI(t), offer, cb, slog.Default())
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	defer br.Close()

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("browser SetRemoteDescription: %v", err)
	}

	dc.OnOpen(func() {
		// 0.5 and -0.5 in Int16 LE.
		_ = dc.Send([]byte{0x00, 0x40, 0x00, 0xC0})
	})

	select {
	case pcm := <-got:
		if len(pcm) != 2 {
			t.Fatalf("expected 2 samples, got %d", len(pcm))
		}
		if pcm[0] < 0.4 || pcm[0] > 0.6 || pcm[1] > -0.4 || pcm[1] < -0.6 {
			t.Fatalf("unexpected samples: %v", pcm)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for PCM over data channel")
	}
}
