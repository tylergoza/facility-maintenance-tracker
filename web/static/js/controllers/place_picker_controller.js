import { Controller } from "@hotwired/stimulus"

// Place dropdowns: a box to narrow a long list of places, the item list
// following the chosen place (items there or anywhere inside it), and on
// the place form, only places that can hold the chosen kind.
//
// Options are hidden and disabled, because iOS ignores hidden options.
export default class extends Controller {
  static targets = ["filter", "place", "item"]

  connect() {
    if (this.hasFilterTarget) this.filterTarget.hidden = false
    this.kindChanged()
    this.filterItems()
  }

  narrow() {
    const query = this.filterTarget.value.trim().toLowerCase()
    this.placeOptions().forEach((option) => {
      option.dataset.narrowed = query && !option.dataset.path.toLowerCase().includes(query) ? "1" : ""
      this.refresh(option)
    })
    // Pick the first match so the choice shows without opening the list.
    const selected = this.placeTarget.selectedOptions[0]
    if (query && (!selected || !selected.value || selected.disabled)) {
      const first = this.placeOptions().find((option) => !option.disabled)
      if (first) {
        this.placeTarget.value = first.value
        this.placeChanged()
      }
    }
  }

  // The kind radios on the place form: a place only fits inside a higher level.
  kindChanged() {
    if (!this.hasPlaceTarget) return
    const kind = this.element.querySelector('input[name="kind"]:checked')
    const level = kind ? Number(kind.dataset.level) : Infinity
    this.placeOptions().forEach((option) => {
      option.dataset.tooLow = Number(option.dataset.level) >= level ? "1" : ""
      this.refresh(option)
    })
    if (this.placeTarget.selectedOptions[0]?.disabled) this.placeTarget.value = ""
  }

  placeChanged() {
    this.filterItems()
  }

  filterItems() {
    if (!this.hasItemTarget || !this.hasPlaceTarget) return
    const place = this.placeTarget.value
    let selectedStillValid = false
    Array.from(this.itemTarget.options).forEach((option) => {
      if (!option.dataset.lineage) return // the "nothing specific" option
      const show = !place || option.dataset.lineage.split(" ").includes(place)
      option.hidden = !show
      option.disabled = !show
      if (show && option.selected) selectedStillValid = true
    })
    if (!selectedStillValid) this.itemTarget.value = ""
  }

  placeOptions() {
    return Array.from(this.placeTarget.options).filter((option) => option.value)
  }

  refresh(option) {
    const off = option.dataset.narrowed === "1" || option.dataset.tooLow === "1"
    option.hidden = off
    option.disabled = off
  }
}
