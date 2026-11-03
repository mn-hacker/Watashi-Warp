package app

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bepass-org/warp-plus/wireguard/conn"
	"github.com/bepass-org/warp-plus/wireguard/device"
	wgtun "github.com/bepass-org/warp-plus/wireguard/tun"
	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
	"github.com/bepass-org/warp-plus/wiresocks"
)

// watashi v1.2.6-ws1: v1.2.6 built a new http.Transport for every attempt,
// never closed a response body and never closed the transport, and retried
// with no pause at all. Every attempt left a keep-alive connection and its two
// goroutines inside the userspace stack. One transport now, no keep-alive,
// every body drained and closed, a short pause between attempts.
func usermodeTunTest(ctx context.Context, l *slog.Logger, tnet *netstack.Net, url string) error {
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(5*time.Second))
	defer cancel()

	tr := &http.Transport{
		DialContext:           tnet.DialContext,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableKeepAlives:     true,
	}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				l.Info("connection test successful")
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		l.Error("connection test failed", "error", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitHandshake(ctx context.Context, l *slog.Logger, dev *device.Device) error {
	lastHandshakeSecs := "0"
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		get, err := dev.IpcGet()
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(get))
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				break
			}

			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}

			if key == "last_handshake_time_sec" {
				lastHandshakeSecs = value
				break
			}
		}
		if lastHandshakeSecs != "0" {
			l.Debug("handshake complete")
			break
		}

		l.Debug("waiting on handshake")
		time.Sleep(1 * time.Second)
	}

	return nil
}

// establishWireguard brings one device up and waits for its handshake.
// watashi v1.2.6-ws1: the device is handed back so the caller can close it
// when the connection test that follows fails. v1.2.6 dropped it on the
// floor, so every failed attempt left a whole wireguard device - its workers,
// its socket and its keepalives - running until the process ended.
func establishWireguard(l *slog.Logger, conf *wiresocks.Configuration, tunDev wgtun.Device, fwmark uint32, t string) (*device.Device, error) {
	// create the IPC message to establish the wireguard conn
	var request bytes.Buffer

	request.WriteString(fmt.Sprintf("private_key=%s\n", conf.Interface.PrivateKey))
	if fwmark != 0 {
		request.WriteString(fmt.Sprintf("fwmark=%d\n", fwmark))
	}

	for _, peer := range conf.Peers {
		request.WriteString(fmt.Sprintf("public_key=%s\n", peer.PublicKey))
		request.WriteString(fmt.Sprintf("persistent_keepalive_interval=%d\n", peer.KeepAlive))
		request.WriteString(fmt.Sprintf("preshared_key=%s\n", peer.PreSharedKey))
		request.WriteString(fmt.Sprintf("endpoint=%s\n", peer.Endpoint))
		request.WriteString(fmt.Sprintf("trick=%s\n", t))
		request.WriteString(fmt.Sprintf("reserved=%d,%d,%d\n", peer.Reserved[0], peer.Reserved[1], peer.Reserved[2]))

		for _, cidr := range peer.AllowedIPs {
			request.WriteString(fmt.Sprintf("allowed_ip=%s\n", cidr))
		}
	}

	dev := device.NewDevice(
		tunDev,
		conn.NewDefaultBind(),
		device.NewSLogger(l.With("subsystem", "wireguard-go")),
	)

	if err := dev.IpcSet(request.String()); err != nil {
		dev.Close()
		return nil, err
	}

	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(15*time.Second))
	defer cancel()
	if err := waitHandshake(ctx, l, dev); err != nil {
		dev.BindClose()
		dev.Close()
		return nil, err
	}

	return dev, nil
}
