export function resolveTheme(pick, osTheme) {
  if (pick === "light" || pick === "dark") return pick
  return osTheme === "light" ? "light" : "dark"
}
