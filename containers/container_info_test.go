package containers

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerInfoLabels(t *testing.T) {
	m := containerInfoMetric(&ContainerMetadata{image: "registry/shop:1.2.3", runtimeID: "4d1f9c", imageID: "sha256:ab12",
		otelServiceName: "shop", otelServiceVersion: "1.2.3"})
	var out dto.Metric
	require.NoError(t, m.Write(&out))
	labels := map[string]string{}
	for _, l := range out.GetLabel() {
		labels[l.GetName()] = l.GetValue()
	}
	assert.Equal(t, map[string]string{
		"image": "registry/shop:1.2.3", "systemd_triggered_by": "", "container_runtime_id": "4d1f9c",
		"image_id": "sha256:ab12", "otel_service_name": "shop", "otel_service_version": "1.2.3",
	}, labels)
}
