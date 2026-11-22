package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// TestConnectionRefResolvesTo verifies that the shorthand (Name) and full (ObjectRef)
// forms of the same connection compare equal — so re-expressing a ref from
// shorthand to ObjectRef isn't mistaken for a migration.
func TestConnectionRefResolvesTo(t *testing.T) {
	apiGroup := "temporal.io"
	// shorthand: namespaced Connection by name
	short := func(name string) temporaliov1alpha1.ConnectionReference {
		return temporaliov1alpha1.ConnectionReference{Name: name}
	}
	// full form: ObjectRef with explicit kind
	full := func(name, kind string) temporaliov1alpha1.ConnectionReference {
		return temporaliov1alpha1.ConnectionReference{
			ObjectRef: &corev1.TypedObjectReference{
				APIGroup: &apiGroup,
				Kind:     kind,
				Name:     name,
			},
		}
	}

	tests := []struct {
		name string
		a, b temporaliov1alpha1.ConnectionReference
		want bool
	}{
		{
			name: "shorthand equals ObjectRef Connection form (normalization)",
			a:    short("foo"),
			b:    full("foo", "Connection"),
			want: true,
		},
		{
			name: "Connection differs from ClusterConnection",
			a:    full("foo", "Connection"),
			b:    full("foo", "ClusterConnection"),
			want: false,
		},
		{
			name: "different names differ",
			a:    short("a"),
			b:    short("b"),
			want: false,
		},
		{
			name: "same cluster ref equals itself",
			a:    full("foo", "ClusterConnection"),
			b:    full("foo", "ClusterConnection"),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.a.ResolvesTo(tc.b))
		})
	}
}
