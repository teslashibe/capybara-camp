import SwiftUI

struct ProviderMark: View {
    let provider: String
    var size: CGFloat = 18
    var body: some View {
        Group {
            if provider == "Claude" {
                Canvas { context, bounds in
                    let pixels = ["00100000100", "00111111100", "01111111110", "11101110111", "11111111111", "00111111100", "00100000100"]
                    let unit = bounds.width / 11
                    for (y, row) in pixels.enumerated() {
                        for (x, pixel) in row.enumerated() where pixel == "1" {
                            context.fill(Path(CGRect(x: CGFloat(x) * unit, y: (bounds.height - unit * 7) / 2 + CGFloat(y) * unit, width: unit, height: unit)), with: .color(Color(hex: 0xB57554)))
                        }
                    }
                }
            } else {
                Image(systemName: provider == "Planning" ? "point.3.connected.trianglepath.dotted" : "terminal")
                    .resizable().scaledToFit().foregroundStyle(provider == "Planning" ? campMuted : campGreen)
            }
        }.frame(width: size, height: size).accessibilityHidden(true)
    }
}
