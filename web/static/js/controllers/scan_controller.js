import { Controller } from "@hotwired/stimulus"

// Reads QR codes with the camera and opens the page they point to: a
// place's or item's "Report a problem", or a supply. Uses the browser's
// own reader where there is one, and jsQR otherwise (Safari).
//
// Only codes for this site are followed, by path, so codes printed with
// the site address still work when it's reached another way.
//
//   <div data-controller="scan" data-scan-origins-value='["https://…"]'>
//     <button data-scan-target="start" data-action="scan#start">
//     <video data-scan-target="video" playsinline muted hidden>
//     <p data-scan-target="status">
export default class extends Controller {
  static targets = ["start", "video", "status"]
  static values = { origins: Array }

  connect() {
    if (!navigator.mediaDevices?.getUserMedia) {
      this.startTarget.hidden = true
      this.say(
        window.isSecureContext
          ? "This browser can't use the camera. Use your phone's camera app to scan the code instead."
          : "The camera only works over https. Use your phone's camera app to scan the code instead.",
      )
    }
    this.pause = () => document.hidden && this.stop()
    document.addEventListener("visibilitychange", this.pause)
  }

  disconnect() {
    document.removeEventListener("visibilitychange", this.pause)
    this.stop()
  }

  async start() {
    this.say("Starting the camera…")
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" }, audio: false })
    } catch (error) {
      this.say(
        error.name === "NotAllowedError"
          ? "The camera is blocked for this site. Allow it in your browser's settings, or use your phone's camera app."
          : "Couldn't start the camera. Use your phone's camera app to scan the code instead.",
      )
      return
    }
    this.videoTarget.srcObject = this.stream
    this.videoTarget.hidden = false
    this.startTarget.hidden = true
    await this.videoTarget.play().catch(() => {})
    try {
      this.decode = await this.decoder()
    } catch {
      this.stop()
      this.say("Couldn't load the QR reader. Check your connection and try again.")
      return
    }
    this.say("Point the camera at a QR code.")
    this.tick()
  }

  stop() {
    clearTimeout(this.timer)
    this.stream?.getTracks().forEach((track) => track.stop())
    this.stream = null
    if (this.hasVideoTarget) this.videoTarget.hidden = true
    if (this.hasStartTarget) this.startTarget.hidden = false
  }

  // decoder returns a function reading a QR code's text from the video,
  // or nothing when there isn't one in view.
  async decoder() {
    if ("BarcodeDetector" in window) {
      const formats = await window.BarcodeDetector.getSupportedFormats().catch(() => [])
      if (formats.includes("qr_code")) {
        const detector = new window.BarcodeDetector({ formats: ["qr_code"] })
        return async (video) => (await detector.detect(video))[0]?.rawValue
      }
    }
    await import(new URL(`../vendor/jsQR.js${new URL(import.meta.url).search}`, import.meta.url))
    const canvas = document.createElement("canvas")
    const context = canvas.getContext("2d", { willReadFrequently: true })
    return async (video) => {
      if (!video.videoWidth) return null
      // Smaller frames read much faster and stickers fill the view anyway.
      const scale = Math.min(1, 640 / Math.max(video.videoWidth, video.videoHeight))
      canvas.width = Math.round(video.videoWidth * scale)
      canvas.height = Math.round(video.videoHeight * scale)
      context.drawImage(video, 0, 0, canvas.width, canvas.height)
      const image = context.getImageData(0, 0, canvas.width, canvas.height)
      return self.jsQR(image.data, image.width, image.height, { inversionAttempts: "dontInvert" })?.data
    }
  }

  tick = async () => {
    if (!this.stream) return
    const text = await this.decode(this.videoTarget).catch(() => null)
    if (text && text !== this.last) {
      this.last = text
      const path = this.destination(text)
      if (path) {
        this.stop()
        this.say("Opening…")
        window.location.assign(path)
        return
      }
      this.say("That code isn't for this site. Try another.")
    }
    this.timer = setTimeout(this.tick, 150)
  }

  // destination is the path on this site a code points to, or null.
  destination(text) {
    let url
    try {
      url = new URL(text)
    } catch {
      return null
    }
    if (url.origin !== window.location.origin && !this.originsValue.includes(url.origin)) return null
    if (!/^\/(report|supplies|items|products|places)(\/|$)/.test(url.pathname)) return null
    return url.pathname + url.search
  }

  say(message) {
    this.statusTarget.textContent = message
  }
}
