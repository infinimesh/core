/*
Copyright © 2021-2023 Infinite Devices GmbH

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
package main

import "strings"

type topicKind int

const (
	topicNone topicKind = iota
	topicReported
	topicDesired
)

func reportedTopic(deviceID string) string {
	return "devices/" + deviceID + "/state/reported"
}

func desiredTopic(deviceID string) string {
	return "devices/" + deviceID + "/state/desired"
}

func classifyTopic(topic, deviceID string) topicKind {
	if deviceID == "" || topic == "" || strings.ContainsAny(topic, "+#") {
		return topicNone
	}
	switch topic {
	case reportedTopic(deviceID):
		return topicReported
	case desiredTopic(deviceID):
		return topicDesired
	default:
		return topicNone
	}
}

// TopicChecker reports whether topic is one this device may use.
// A device may publish reported state and subscribe to desired state
// for its own id. Any other topic, including another device's, is denied.
func TopicChecker(topic, deviceID string) bool {
	return classifyTopic(topic, deviceID) != topicNone
}
