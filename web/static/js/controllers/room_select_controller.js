import { Controller } from "@hotwired/stimulus"

// Limits the room dropdown to rooms in the chosen building and, when there
// is an item dropdown, the items to those in the chosen room (or anywhere
// in the building when no room is chosen).
export default class extends Controller {
  static targets = ["building", "room", "item"]

  connect() {
    this.filter()
  }

  filter() {
    const buildingId = this.buildingTarget.value
    let selectedStillValid = false

    Array.from(this.roomTarget.options).forEach((option) => {
      if (!option.dataset.buildingId) return // the "no room" option
      const show = option.dataset.buildingId === buildingId
      option.hidden = !show
      option.disabled = !show
      if (show && option.selected) selectedStillValid = true
    })

    if (!selectedStillValid) this.roomTarget.value = ""
    this.roomTarget.disabled = !buildingId
    this.filterItems()
  }

  filterItems() {
    if (!this.hasItemTarget) return
    const buildingId = this.buildingTarget.value
    const roomId = this.roomTarget.value
    let selectedStillValid = false

    Array.from(this.itemTarget.options).forEach((option) => {
      if (!option.dataset.buildingId) return // the "nothing specific" option
      const show = option.dataset.buildingId === buildingId && (!roomId || option.dataset.roomId === roomId)
      option.hidden = !show
      option.disabled = !show
      if (show && option.selected) selectedStillValid = true
    })

    if (!selectedStillValid) this.itemTarget.value = ""
    this.itemTarget.disabled = !buildingId
  }
}
