import SwiftUI
import AppKit

struct ProviderMark: View {
    let provider: String
    var size: CGFloat = 18
    var body: some View {
        Group {
            if provider == "Claude" {
                Image(nsImage: Self.claudeImage).resizable().interpolation(.none).scaledToFit()
            } else {
                Image(systemName: provider == "Planning" ? "point.3.connected.trianglepath.dotted" : "terminal")
                    .resizable().scaledToFit().foregroundStyle(provider == "Planning" ? campMuted : campGreen)
            }
        }.frame(width: size, height: size).accessibilityHidden(true)
    }
    // Native Menu labels render an Image but omit a SwiftUI Canvas. Cache the
    // same robot pixels once so menus, cards and chat retain the same mark.
    private static let claudeImage = NSImage(size: NSSize(width: 22, height: 22), flipped: true) { _ in
        NSColor(red: 0xB5 / 255.0, green: 0x75 / 255.0, blue: 0x54 / 255.0, alpha: 1).setFill()
        let pixels = ["00100000100", "00111111100", "01111111110", "11101110111", "11111111111", "00111111100", "00100000100"]
        for (y, row) in pixels.enumerated() {
            for (x, pixel) in row.enumerated() where pixel == "1" {
                NSBezierPath(rect: NSRect(x: x * 2, y: y * 2 + 4, width: 2, height: 2)).fill()
            }
        }
        return true
    }
}
