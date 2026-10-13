export function indexNamespaces(items) {
  const next = {}
  for (const namespace of items || []) {
    if (namespace && namespace.uuid) next[namespace.uuid] = namespace
  }
  return next
}
