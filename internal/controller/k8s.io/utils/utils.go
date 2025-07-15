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
	"fmt"
	"hash"
	"hash/fnv"

	"github.com/davecgh/go-spew/spew"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

// The config MUST NOT be changed because that could change the result of a hash operation
var prettyPrintConfigForHash = &spew.ConfigState{
	Indent:                  " ",
	SortKeys:                true,
	DisableMethods:          true,
	SpewKeys:                true,
	DisablePointerAddresses: true,
	DisableCapacities:       true,
}

// dumpForHash keeps the original Spew.Sprintf format to ensure the same checksum
func dumpForHash(a interface{}) string {
	return prettyPrintConfigForHash.Sprintf("%#v", a)
}

// deepHashObject writes specified object to hash using the spew library
// which follows pointers and prints actual values of the nested objects
// ensuring the hash does not change when a pointer changes.
func deepHashObject(hasher hash.Hash, objectToWrite interface{}) {
	hasher.Reset()
	fmt.Fprintf(hasher, "%v", dumpForHash(objectToWrite))
}

// ComputeHash returns a hash value calculated from pod template and
// a collisionCount to avoid hash collision. The hash will be safe encoded to
// avoid bad words.
// If `short` is true, the hash is truncated to 4 digits
//
// Copied from https://github.com/kubernetes/kubernetes/blob/86fec81606b579cc478a30656c29ddb400a72dc6/pkg/controller/controller_utils.go#L1174
func ComputeHash(template *v1.PodTemplateSpec, collisionCount *int32, short bool) string {
	podTemplateSpecHasher := fnv.New32a()
	deepHashObject(podTemplateSpecHasher, *template)

	// Add collisionCount in the hash if it exists.
	if collisionCount != nil {
		collisionCountBytes := make([]byte, 8)
		binary.LittleEndian.PutUint32(collisionCountBytes, uint32(*collisionCount))
		_, _ = podTemplateSpecHasher.Write(collisionCountBytes)
	}

	if short {
		return rand.SafeEncodeString(fmt.Sprintf("%04d", podTemplateSpecHasher.Sum32()%10000))
	}
	return rand.SafeEncodeString(fmt.Sprint(podTemplateSpecHasher.Sum32()))
}
