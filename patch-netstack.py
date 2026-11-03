#!/usr/bin/env python3
# watashi v1.2.6-ws1: bound the TCP buffers of the userspace stack.
#
# gVisor gives every TCP endpoint a 1 MiB receive and send buffer by default
# and does not shrink them. A WARP proxy holds hundreds of TCP endpoints at
# once, and whenever the app behind the proxy reads slower than the far end
# sends (a speed test, a big download to a phone), each endpoint fills its
# whole window - which is how one speed test froze a 1 GB server (upstream
# issue #43). Here they start at 128 KiB and are allowed to grow to 2 MiB by
# receive-buffer moderation, which only grows a buffer the app is actually
# draining fast, so a fast flow still gets its window and a stalled one does
# not sit on a megabyte.
import io, re, sys

path = sys.argv[1]
src = io.open(path, encoding='utf-8').read()
if 'watashi v1.2.6-ws1' in src:
    print('netstack: already patched'); sys.exit(0)
anchor = re.search(r'return nil, nil, fmt\.Errorf\("could not enable TCP SACK: %v", tcpipErr\)\s*\n\s*\}\n', src)
if not anchor:
    print('netstack: the SACK block was not found, nothing changed'); sys.exit(1)
block = '''	// watashi v1.2.6-ws1: see patch-netstack.py - bounded, self-tuning TCP buffers.
	rcvOpt := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 128 << 10, Max: 2 << 20}
	if tcpipErr := dev.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &rcvOpt); tcpipErr != nil {
		return nil, nil, fmt.Errorf("could not set the TCP receive buffer: %v", tcpipErr)
	}
	sndOpt := tcpip.TCPSendBufferSizeRangeOption{Min: 4 << 10, Default: 128 << 10, Max: 2 << 20}
	if tcpipErr := dev.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &sndOpt); tcpipErr != nil {
		return nil, nil, fmt.Errorf("could not set the TCP send buffer: %v", tcpipErr)
	}
	modOpt := tcpip.TCPModerateReceiveBufferOption(true)
	if tcpipErr := dev.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &modOpt); tcpipErr != nil {
		return nil, nil, fmt.Errorf("could not enable TCP receive buffer moderation: %v", tcpipErr)
	}
'''
src = src[:anchor.end()] + block + src[anchor.end():]
io.open(path, 'w', encoding='utf-8').write(src)
print('netstack: TCP buffers bounded')
