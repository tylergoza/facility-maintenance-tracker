import { Controller } from "@hotwired/stimulus"

// On the item form, an item's name picks its product. When the name is
// one already in the list, the product's category and counting are used,
// so their fields give way to a line saying so.
//
//   <form data-controller="product" data-action="input->product#update">
//     <input name="name" data-product-target="name" list="products">
//     <datalist id="products"><option value="Chairs" data-id="3" data-category="Furniture" data-counted="1">
//     <div data-product-target="fields">…category, tracking radios…</div>
//     <p data-product-target="summary" hidden></p>
export default class extends Controller {
  static targets = ["name", "fields", "summary"]

  connect() {
    this.update()
  }

  update() {
    const name = this.nameTarget.value.trim().toLowerCase()
    const match = [...(this.nameTarget.list?.options || [])].find(
      (option) => option.dataset.id && option.value.toLowerCase() === name,
    )
    this.fieldsTarget.hidden = !!match
    this.fieldsTarget.querySelectorAll("input, select").forEach((el) => (el.disabled = !!match))
    this.summaryTarget.hidden = !match
    // The counting chosen for a new product, put back when the name stops
    // matching one that has its own.
    const checked = this.element.querySelector('[name="tracking"]:checked')
    if (!match && this.chosen) {
      const own = this.element.querySelector(`[name="tracking"][value="${this.chosen}"]`)
      if (own) own.checked = true
      this.chosen = null
    } else if (match && !this.chosen && checked) {
      this.chosen = checked.value
    }
    if (match) {
      const { id, category, counted } = match.dataset
      this.summaryTarget.replaceChildren(
        `${match.value} is a product you already have (${[category, counted ? "counted" : "tracked one by one"].filter(Boolean).join(", ")}). `,
        "Change its category or counting on ",
        Object.assign(document.createElement("a"), { href: `/products/${id}`, textContent: "its page" }),
        ".",
      )
      const tracking = this.element.querySelector(`[name="tracking"][value="${counted ? "count" : "each"}"]`)
      if (tracking) tracking.checked = true
    }
    // Let sections that depend on the counting (unit IDs) catch up.
    this.element.dispatchEvent(new Event("change", { bubbles: true }))
  }
}
