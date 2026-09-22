package main

import (
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"

	"wacalls/internal/voip/media"

	"github.com/pion/webrtc/v4"
)

// pcmChannelLabel is the data channel the browser opens to carry raw 16 kHz mono
// Int16 LE PCM in both directions. The browser side must create it with this label.
const pcmChannelLabel = "pcm"

// BridgeCallbacks are handed to NewBridge instead of being assigned afterwards:
// pion drives its handlers from its own goroutines and can fire them before the
// constructor returns, so a field written by the caller at that point is a race.
type BridgeCallbacks struct {
	// OnBrowserPCM is invoked with decoded 16 kHz mono PCM captured from the browser mic.
	OnBrowserPCM func(pcm []float32)
	// OnTerminalICE fires when the peer connection fails or closes.
	OnTerminalICE func()
}

// iceConfig describes how this server is reachable from a browser.
//
// It matters the moment the server runs behind NAT — which every container on a
// bridge network is. Left empty, pion advertises the addresses of the interfaces
// it can see; inside a container those are private (172.x) and a browser on the
// internet can never reach them. Everything else keeps working, so the only
// symptom is that audio never connects.
type iceConfig struct {
	// PublicIP is advertised to the browser in place of the local address.
	PublicIP string
	// UDPPortMin/Max pin media to a known range so it can be published and
	// opened on the firewall. Zero means pion picks any free port.
	UDPPortMin int
	UDPPortMax int
}

func (c iceConfig) validate() error {
	if c.PublicIP != "" && net.ParseIP(c.PublicIP) == nil {
		return fmt.Errorf("public ip %q is not an IP address", c.PublicIP)
	}
	if c.UDPPortMin == 0 && c.UDPPortMax == 0 {
		return nil
	}
	if c.UDPPortMin <= 0 || c.UDPPortMax <= 0 {
		return fmt.Errorf("udp port range needs both ends, got %d-%d", c.UDPPortMin, c.UDPPortMax)
	}
	if c.UDPPortMin > 65535 || c.UDPPortMax > 65535 {
		return fmt.Errorf("udp port range %d-%d is out of range", c.UDPPortMin, c.UDPPortMax)
	}
	if c.UDPPortMin > c.UDPPortMax {
		return fmt.Errorf("udp port range %d-%d is inverted", c.UDPPortMin, c.UDPPortMax)
	}
	return nil
}

// newBrowserAPI builds the WebRTC stack used for browser connections.
//
// Only the browser leg gets this. The relay leg in internal/voip/transport dials
// out to WhatsApp and rewrites its own SDP to force the relay candidate, so
// rewriting its addresses here would fight that.
func newBrowserAPI(cfg iceConfig) (*webrtc.API, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	if cfg.PublicIP != "" {
		// Replaces the host candidate's address, which is what makes a container
		// behind NAT reachable.
		se.SetNAT1To1IPs([]string{cfg.PublicIP}, webrtc.ICECandidateTypeHost)

		// NAT1To1 only rewrites candidates of the same family as the address it
		// was given, so with an IPv4 public address any IPv6 interface would be
		// advertised untouched — a private container address the browser would
		// try and wait out before falling back. One public address means one
		// family.
		if ip := net.ParseIP(cfg.PublicIP); ip.To4() != nil {
			se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		} else {
			se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP6})
		}
	}
	if cfg.UDPPortMin > 0 {
		if err := se.SetEphemeralUDPPortRange(uint16(cfg.UDPPortMin), uint16(cfg.UDPPortMax)); err != nil {
			return nil, fmt.Errorf("udp port range: %w", err)
		}
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(se)), nil
}

// Bridge is the browser-leg adapter: it carries raw PCM between the browser and
// the CallManager over a WebRTC data channel. The call core only ever sees
// []float32 PCM, so it stays unaware of the transport (no Opus here anymore).
type Bridge struct {
	pc  *webrtc.PeerConnection
	dc  atomic.Pointer[webrtc.DataChannel]
	cb  BridgeCallbacks
	log *slog.Logger
}

func NewBridge(api *webrtc.API, offerSDP string, cb BridgeCallbacks, log *slog.Logger) (*Bridge, string, error) {
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	br := &Bridge{pc: pc, cb: cb, log: log}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != pcmChannelLabel {
			return
		}
		br.dc.Store(dc)
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if br.cb.OnBrowserPCM != nil && len(msg.Data) > 0 {
				br.cb.OnBrowserPCM(media.PCMInt16LEToFloat32(msg.Data))
			}
		})
	})

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Debug("browser ice state", "state", s.String())
		if s == webrtc.ICEConnectionStateFailed || s == webrtc.ICEConnectionStateClosed {
			if br.cb.OnTerminalICE != nil {
				br.cb.OnTerminalICE()
			}
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		pc.Close()
		return nil, "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, "", err
	}
	<-gatherComplete

	return br, pc.LocalDescription().SDP, nil
}

// WritePCM sends 16 kHz mono float32 PCM to the browser as Int16 LE over the data
// channel. It is a no-op until the channel is open.
func (b *Bridge) WritePCM(pcm []float32) error {
	dc := b.dc.Load()
	if dc == nil || len(pcm) == 0 {
		return nil
	}
	return dc.Send(media.PCMFloat32ToInt16LE(pcm))
}

func (b *Bridge) Close() {
	if b.pc != nil {
		_ = b.pc.Close()
	}
}
