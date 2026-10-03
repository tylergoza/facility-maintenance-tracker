import { Controller } from "@hotwired/stimulus"

// Fills in how many of a supply were used from the units ticked in a
// "Which ones" picker: each ticked unit takes `per`. Unticking them all
// leaves the number alone so it can still be typed by hand.
//
//   <form data-controller="unit-count" data-unit-count-per-value="1">
//     <input type="checkbox" name="units" data-unit-count-target="unit" data-action="unit-count#update">
//     <input name="amount" data-unit-count-target="amount">
export default class extends Controller {
  static targets = ["unit", "amount"]
  static values = { per: Number }

  update() {
    if (!this.hasAmountTarget || !this.perValue) return
    const ticked = this.unitTargets.filter((unit) => unit.checked).length
    if (ticked > 0) this.amountTarget.value = ticked * this.perValue
  }
}
