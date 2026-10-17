package metrics

import "testing"

func TestCounters(t *testing.T) {
	BasicAuthAcceptedTotal.Inc()
	BasicAuthFailedToAcceptTotal.Inc()
	BasicAuthDeviceAuthFailedTotal.Inc()
	TlsAcceptedTotal.Inc()
	TlsFailedToAcceptTotal.Inc()
	TlsDeviceAuthFailedTotal.Inc()
	ActiveConnectionsTotal.Inc()
	ActiveConnectionsTotal.Dec()
	ConnNotAnMqttPacketTotal.Inc()
}
