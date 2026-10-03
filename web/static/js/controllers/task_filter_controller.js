import { Controller } from "@hotwired/stimulus"

// Client-side filtering of the dashboard: free-text search plus the status
// tiles, which act as toggle buttons. State is kept in the URL hash so a
// filtered view can be bookmarked or shared.
export default class extends Controller {
  static targets = ["card", "group", "tile", "query", "clear", "none"]

  connect() {
    const params = new URLSearchParams(location.hash.slice(1))
    this.statuses = new Set((params.get("status") || "").split(",").filter(Boolean))
    if (this.hasQueryTarget && params.get("q")) this.queryTarget.value = params.get("q")
    this.filter()
  }

  toggleStatus(event) {
    const status = event.currentTarget.dataset.status
    this.statuses.has(status) ? this.statuses.delete(status) : this.statuses.add(status)
    this.filter()
  }

  clear() {
    this.statuses.clear()
    if (this.hasQueryTarget) this.queryTarget.value = ""
    this.filter()
  }

  filter() {
    const terms = this.#query().split(/\s+/).filter(Boolean)
    let visible = 0

    this.cardTargets.forEach((card) => {
      const haystack = card.dataset.search.toLowerCase()
      const matches =
        (this.statuses.size === 0 || this.statuses.has(card.dataset.status)) &&
        terms.every((term) => haystack.includes(term))
      card.hidden = !matches
      if (matches) visible++
    })

    this.groupTargets.forEach((group) => {
      group.hidden = !group.querySelector("[data-task-filter-target='card']:not([hidden])")
    })

    this.tileTargets.forEach((tile) => {
      tile.setAttribute("aria-pressed", String(this.statuses.has(tile.dataset.status)))
    })

    const filtering = this.statuses.size > 0 || terms.length > 0
    if (this.hasClearTarget) this.clearTarget.hidden = !filtering
    if (this.hasNoneTarget) this.noneTarget.hidden = visible > 0 || this.cardTargets.length === 0
    this.#saveHash()
  }

  #query() {
    return this.hasQueryTarget ? this.queryTarget.value.trim().toLowerCase() : ""
  }

  #saveHash() {
    const params = new URLSearchParams()
    if (this.statuses.size) params.set("status", [...this.statuses].join(","))
    if (this.#query()) params.set("q", this.#query())
    const hash = params.toString()
    history.replaceState(null, "", hash ? `#${hash}` : location.pathname + location.search)
  }
}
