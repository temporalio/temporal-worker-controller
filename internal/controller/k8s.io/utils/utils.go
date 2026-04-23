// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

// ComputeHash returns a hash value calculated from pod template and
// a collisionCount to avoid hash collision. The hash will be safe encoded to
// avoid bad words.
// If `short` is true, the hash is truncated to 4 digits
//
// Copied from https://github.com/kubernetes/kubernetes/blob/86fec81606b579cc478a30656c29ddb400a72dc6/pkg/controller/controller_utils.go#L1174
func ComputeHash(template *corev1.PodTemplateSpec, collisionCount *int32, short bool) string {
	podTemplateSpecHasher := fnv.New32a()
	// json.Marshal on corev1.PodTemplateSpec never returns an error; omitempty keeps
	// the output stable across k8s API upgrades that only add optional fields.
	data, _ := json.Marshal(*template)
	_, _ = podTemplateSpecHasher.Write(data)

	// Add collisionCount in the hash if it exists.
	if collisionCount != nil {
		collisionCountBytes := make([]byte, 8)
		binary.LittleEndian.PutUint32(collisionCountBytes, uint32(*collisionCount))
		_, _ = podTemplateSpecHasher.Write(collisionCountBytes)
	}

	if short {
		return rand.SafeEncodeString(fmt.Sprintf("%04d", podTemplateSpecHasher.Sum32()%10000))
	}
	return rand.SafeEncodeString(fmt.Sprintf("%010d", podTemplateSpecHasher.Sum32()%10000))
}
