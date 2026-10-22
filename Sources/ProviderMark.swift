import SwiftUI
import AppKit

struct ProviderMark: View {
    let provider: String
    var size: CGFloat = 18
    var muted = false
    static func wash(_ provider: String) -> Color { Color(hex: provider == "Claude" ? 0xFBEDE5 : provider == "Planning" ? 0xF1EAF8 : 0xEAF2EC) }
    static func ink(_ provider: String) -> Color { Color(hex: provider == "Claude" ? 0xAD5636 : provider == "Planning" ? 0x75539D : 0x396C4D) }
    var body: some View {
        Group {
            if muted {
                if provider == "Claude" {
                    Image(nsImage: Self.image(provider: provider, size: size, muted: true)).resizable().scaledToFit()
                } else {
                    Image(nsImage: Self.image(provider: provider, size: size)).renderingMode(.template)
                        .resizable().scaledToFit().foregroundStyle(campMuted)
                }
            } else { Image(nsImage: Self.image(provider: provider, size: size)).resizable().scaledToFit() }
        }.frame(width: size, height: size).accessibilityHidden(true)
    }
    private static let scaledImages = NSCache<NSString, NSImage>()
    private static func image(provider: String, size: CGFloat, muted: Bool = false) -> NSImage {
        let key = "\(provider):\(size):\(muted)" as NSString
        if let cached = scaledImages.object(forKey: key) { return cached }
        let source = provider == "Claude" ? (muted ? mutedClaudeImage : claudeImage) : provider == "Planning" ? planningImage : codexImage
        // AppKit menus use the image's intrinsic size, even when a SwiftUI
        // frame is smaller. Cache the correct intrinsic size for that surface.
        let result = NSImage(size: NSSize(width: size, height: size), flipped: true) { _ in
            source.draw(in: NSRect(x: 0, y: 0, width: size, height: size))
            return true
        }
        scaledImages.setObject(result, forKey: key)
        return result
    }
    // These paths match the approved mock. Native Menu labels render NSImage
    // reliably, so every card, menu and chat reuses the same cached marks.
    private static let claudeImage = makeClaudeImage(0xAD5636)
    private static let mutedClaudeImage = makeClaudeImage(0x9C664F)
    private static func makeClaudeImage(_ fill: Int) -> NSImage {
      NSImage(size: NSSize(width: 22, height: 22), flipped: true) { _ in
        color(fill).setFill()
        let path = NSBezierPath()
        let points: [NSPoint] = [(5,3),(17,3),(17,6),(20,6),(20,15),(17,15),(17,19),(15,19),(15,15),(12,15),(12,19),(10,19),(10,15),(7,15),(7,19),(5,19),(5,15),(2,15),(2,6),(5,6)].map { NSPoint(x: $0.0, y: $0.1) }
        path.move(to: points[0])
        for point in points.dropFirst() { path.line(to: point) }
        path.close(); path.fill()
        NSColor.white.setFill()
        for x in [6,14] { NSBezierPath(rect: NSRect(x: x, y: 8, width: 2, height: 3)).fill() }
        return true
      }
    }
    private static let codexImage = NSImage(size: NSSize(width: 22, height: 22), flipped: true) { _ in
        color(0x396C4D).setStroke()
        let frame = NSBezierPath(roundedRect: NSRect(x: 2, y: 3, width: 19, height: 16), xRadius: 2, yRadius: 2)
        frame.lineWidth = 1.45; frame.stroke()
        let path = NSBezierPath()
        path.move(to: NSPoint(x: 6, y: 7)); path.line(to: NSPoint(x: 9, y: 10)); path.line(to: NSPoint(x: 6, y: 13))
        path.move(to: NSPoint(x: 12, y: 13)); path.line(to: NSPoint(x: 16, y: 13))
        path.lineWidth = 1.45; path.lineCapStyle = .round; path.lineJoinStyle = .round; path.stroke()
        return true
    }
    private static let planningImage = NSImage(size: NSSize(width: 22, height: 22), flipped: true) { _ in
        color(0x75539D).setStroke()
        let path = NSBezierPath()
        path.move(to: NSPoint(x: 6, y: 18)); path.line(to: NSPoint(x: 16, y: 18))
        path.move(to: NSPoint(x: 7, y: 14))
        path.curve(to: NSPoint(x: 11, y: 3), controlPoint1: NSPoint(x: 3, y: 10), controlPoint2: NSPoint(x: 5, y: 3))
        path.curve(to: NSPoint(x: 15, y: 14), controlPoint1: NSPoint(x: 17, y: 3), controlPoint2: NSPoint(x: 19, y: 10))
        path.line(to: NSPoint(x: 14, y: 16)); path.line(to: NSPoint(x: 8, y: 16)); path.close()
        path.lineWidth = 1.45; path.lineCapStyle = .round; path.lineJoinStyle = .round; path.stroke()
        return true
    }
    private static func color(_ hex: Int) -> NSColor {
        NSColor(red: CGFloat((hex >> 16) & 255) / 255, green: CGFloat((hex >> 8) & 255) / 255,
                blue: CGFloat(hex & 255) / 255, alpha: 1)
    }
}

struct ProviderBadge: View {
    let provider: String
    var body: some View {
        HStack(spacing: 5) {
            ProviderMark(provider: provider, size: 14)
            Text(provider).font(.system(size: 10, weight: .medium))
        }.padding(.horizontal, 6).padding(.vertical, 3)
            .background(ProviderMark.wash(provider)).clipShape(RoundedRectangle(cornerRadius: 5))
    }
}
