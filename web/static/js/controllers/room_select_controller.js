import { Controller } from "@hotwired/stimulus"

// Limits the room dropdown to rooms in the chosen building.
export default class extends Controller {
  static targets = ["building", "room"]

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
  }
}
