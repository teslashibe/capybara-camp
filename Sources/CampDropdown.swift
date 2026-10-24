import AppKit
import SwiftUI

struct CampMenuEntry {
    let title: String
    var checked = false
    var action: (() -> Void)? = nil
}

// The compact card menu and the full field use the same options and actions.
struct CampMenuEntries: View {
    let entries: [CampMenuEntry]
    var body: some View {
        ForEach(Array(entries.enumerated()), id: \.offset) { _, entry in
            if let action = entry.action {
                Button(action: action) {
                    HStack { Text(entry.title); if entry.checked { Image(systemName: "checkmark") } }
                }
            } else { Text(entry.title) }
        }
    }
}

struct CampDropdown: View {
    @Environment(\.isEnabled) private var isEnabled
    @State private var trigger = CampMenuTrigger()
    let title: String
    let accessibilityLabel: String
    var icon: String? = nil
    let entries: [CampMenuEntry]
    var body: some View {
        CampDropdownLabel(title: title, icon: icon).font(.system(size: 11))
            .accessibilityHidden(true).allowsHitTesting(false)
            .overlay {
                CampNativeMenu(title: title, label: accessibilityLabel, entries: entries, trigger: trigger)
                    .focusable(isEnabled)
                    .onKeyPress(keys: [.space, .return, .downArrow]) { press in
                        guard isEnabled, press.modifiers.intersection([.command, .control, .option]).isEmpty,
                              let button = trigger.button else { return .ignored }
                        button.performClick(nil)
                        return .handled
                    }
            }
    }
}

private final class CampMenuTrigger {
    weak var button: NSButton?
}

// AppKit owns menu tracking, arrow keys, Escape and VoiceOver activation.
// SwiftUI supplies the shared field drawing; the transparent native button
// covers its complete bounds and supplies the standard keyboard focus ring.
private struct CampNativeMenu: NSViewRepresentable {
    @Environment(\.isEnabled) private var isEnabled
    let title: String
    let label: String
    let entries: [CampMenuEntry]
    let trigger: CampMenuTrigger

    final class Action: NSObject {
        let invoke: () -> Void
        init(_ invoke: @escaping () -> Void) { self.invoke = invoke }
    }

    final class Button: NSButton {
        var entries: [CampMenuEntry] = []
        override var acceptsFirstResponder: Bool { isEnabled }
        override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
        override func draw(_ dirtyRect: NSRect) {}
        override var focusRingMaskBounds: NSRect { bounds }
        override func drawFocusRingMask() {
            NSBezierPath(roundedRect: bounds, xRadius: 6, yRadius: 6).fill()
        }
        override func keyDown(with event: NSEvent) {
            let modifiers = event.modifierFlags.intersection([.command, .control, .option])
            if modifiers.isEmpty && [UInt16(36), 49, 76, 125].contains(event.keyCode) {
                if isEnabled { performClick(nil) }
            } else { super.keyDown(with: event) }
        }
        @objc func openMenu(_ sender: Any?) {
            guard isEnabled else { return }
            let menu = NSMenu()
            menu.autoenablesItems = false
            menu.minimumWidth = bounds.width
            for entry in entries {
                let item = NSMenuItem(title: entry.title, action: nil, keyEquivalent: "")
                item.isEnabled = entry.action != nil
                item.state = entry.checked ? .on : .off
                if let action = entry.action {
                    item.target = self
                    item.action = #selector(selectEntry(_:))
                    item.representedObject = Action(action)
                }
                menu.addItem(item)
            }
            menu.popUp(positioning: nil, at: NSPoint(x: 0, y: 0), in: self)
        }
        @objc func selectEntry(_ item: NSMenuItem) {
            (item.representedObject as? Action)?.invoke()
        }
    }

    func makeNSView(context: Context) -> Button {
        let button = Button(frame: .zero)
        button.title = ""
        button.isBordered = false
        button.focusRingType = .exterior
        button.setButtonType(.momentaryPushIn)
        button.target = button
        button.action = #selector(Button.openMenu(_:))
        button.setAccessibilityRole(.popUpButton)
        trigger.button = button
        return button
    }
    func updateNSView(_ button: Button, context: Context) {
        button.entries = entries
        button.isEnabled = isEnabled
        button.setAccessibilityLabel(label)
        button.setAccessibilityValue(title)
        button.toolTip = label
    }
}
