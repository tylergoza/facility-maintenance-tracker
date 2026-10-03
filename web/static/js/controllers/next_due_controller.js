import { Controller } from "@hotwired/stimulus"
import { addInterval, formatDate } from "./dates.js"

// "Mark done" form: previews when the task will next be due based on the
// date the work was done.
export default class extends Controller {
  static targets = ["performed", "next", "hint"]
  static values = { intervalValue: Number, intervalUnit: String, last: String }

  connect() {
    this.update()
  }

  update() {
    if (!this.intervalValueValue || !this.intervalUnitValue) return
    const performed = this.performedTarget.value
    if (!performed) return
    // Back-dated entries don't move "last done" backwards (matches server).
    const base = this.lastValue && this.lastValue > performed ? this.lastValue : performed
    const next = addInterval(base, this.intervalValueValue, this.intervalUnitValue)
    if (!next) return
    this.hintTarget.textContent = `Leave blank to schedule it for ${formatDate(next)}, or pick a different date.`
  }
}
