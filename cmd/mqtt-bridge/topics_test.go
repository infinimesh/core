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

import "testing"

func TestClassifyTopic(t *testing.T) {
	id := "dev-1"
	if classifyTopic(reportedTopic(id), id) != topicReported {
		t.Fatal("reported")
	}
	if classifyTopic(desiredTopic(id), id) != topicDesired {
		t.Fatal("desired")
	}
	denied := []string{
		desiredTopic("other"),
		reportedTopic("other"),
		"shadow/devices/" + id + "/reported",
		"shadow/devices/" + id + "/desired",
		"devices/" + id + "/state/desired/extra",
		"devices/+/state/reported",
		"devices/" + id + "/state/#",
		"",
	}
	for _, topic := range denied {
		if classifyTopic(topic, id) != topicNone {
			t.Fatalf("accepted %q", topic)
		}
	}
	if TopicChecker(reportedTopic(id), id) != true || TopicChecker("nope", id) != false {
		t.Fatal("TopicChecker")
	}
}
