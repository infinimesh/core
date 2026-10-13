/*
Copyright © 2021-2023 Infinite Devices GmbH, Nikita Ivanovski info@slnt-opp.xyz

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
package infinimesh

type ContextKey string

const INFINIMESH_ROOT_CLAIM = "root"
const INFINIMESH_ACCOUNT_CLAIM = "account"

const INFINIMESH_SESSION_CLAIM = "session"
const INFINIMESH_NOSESSION_CLAIM = "nosession"

const INFINIMESH_DEVICES_CLAIM = "devices"
const INFINIMESH_STREAM_CLAIM = "stream"
const INFINIMESH_NAMESPACE_CLAIM = "namespace"
const INFINIMESH_POST_CLAIM = "post"
const INFINIMESH_JTI_CLAIM = "jti"
const INFINIMESH_PLUGIN_CLAIM = "plugin"
const INFINIMESH_OTA_PROVIDER_CLAIM = "ota_provider"

const InfinimeshRootCtxKey = ContextKey(INFINIMESH_ROOT_CLAIM)
const InfinimeshAccountCtxKey = ContextKey(INFINIMESH_ACCOUNT_CLAIM)
const InfinimeshSessionCtxKey = ContextKey(INFINIMESH_SESSION_CLAIM)
const InfinimeshDevicesCtxKey = ContextKey(INFINIMESH_DEVICES_CLAIM)
const InfinimeshStreamCtxKey = ContextKey(INFINIMESH_STREAM_CLAIM)
const InfinimeshNamespaceCtxKey = ContextKey(INFINIMESH_NAMESPACE_CLAIM)
const InfinimeshPostCtxKey = ContextKey(INFINIMESH_POST_CLAIM)
const InfinimeshJTICtxKey = ContextKey(INFINIMESH_JTI_CLAIM)
const InfinimeshPluginCtxKey = ContextKey(INFINIMESH_PLUGIN_CLAIM)
const InfinimeshOTAProviderCtxKey = ContextKey(INFINIMESH_OTA_PROVIDER_CLAIM)
