// Date helpers shared by controllers. Mirrors store.AddInterval on the
// server: months/years clamp to the end of shorter months (Jan 31 + 1 month
// = Feb 28/29). Dates are "YYYY-MM-DD" strings handled in UTC.
// (Not a controller: no "_controller.js" suffix, so the autoloader ignores it.)

export function addInterval(date, value, unit) {
  const d = parse(date)
  if (!d || !(value > 0)) return null
  switch (unit) {
    case "days":
      d.setUTCDate(d.getUTCDate() + value)
      break
    case "weeks":
      d.setUTCDate(d.getUTCDate() + 7 * value)
      break
    case "months":
      return iso(addMonths(d, value))
    case "years":
      return iso(addMonths(d, 12 * value))
    default:
      return null
  }
  return iso(d)
}

function addMonths(d, months) {
  const first = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + months, 1))
  const lastDay = new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + 1, 0)).getUTCDate()
  first.setUTCDate(Math.min(d.getUTCDate(), lastDay))
  return first
}

function parse(date) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date || "")
  return m ? new Date(Date.UTC(+m[1], +m[2] - 1, +m[3])) : null
}

function iso(d) {
  return d.toISOString().slice(0, 10)
}

export function formatDate(date) {
  const d = parse(date)
  return d ? d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" }) : date
}
