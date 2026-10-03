import { Controller } from "@hotwired/stimulus"
import { addInterval, formatDate } from "./dates.js"

// Task form: shows/hides the repeat interval fields and previews the next
// due date that the server will calculate if "Next due" is left blank.
export default class extends Controller {
  static targets = ["toggle", "fields", "value", "unit", "last", "next", "hint"]

  connect() {
    this.defaultHint = this.hintTarget.textContent
    this.update()
  }

  update() {
    const recurring = this.toggleTarget.checked
    this.fieldsTarget.hidden = !recurring
    this.valueTarget.required = recurring

    const last = this.lastTarget.value
    if (recurring && last) {
      const next = addInterval(last, Number(this.valueTarget.value), this.unitTarget.value)
      this.nextTarget.placeholder = next || ""
      this.hintTarget.textContent = next
        ? `If left blank, next due will be ${formatDate(next)}.`
        : this.defaultHint
    } else if (!recurring) {
      this.hintTarget.textContent = "One-time task: set when it's due. It closes after it's marked done."
    } else {
      this.hintTarget.textContent = this.defaultHint
    }
  }
}
