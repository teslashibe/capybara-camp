#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"
export PYTHONDONTWRITEBYTECODE=1
python3 -c 'import sys; sys.exit("Python 3.11 or newer is required") if sys.version_info < (3, 11) else None'
mkdir -p build
python3 -m unittest discover -s Tests -v
swiftc -parse-as-library -D CAMP_TESTING -swift-version 5 -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift Tests/CampStateTests.swift -o build/CampStateTests
build/CampStateTests
swiftc -parse-as-library -D CAMP_TESTING -swift-version 5 -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift Tests/CampCloudTests.swift -o build/CampCloudTests
build/CampCloudTests
swiftc -parse-as-library -D CAMP_TESTING -swift-version 5 -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift Tests/CampSharingTests.swift -o build/CampSharingTests
build/CampSharingTests
swiftc -parse-as-library -D CAMP_TESTING -swift-version 5 -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift Tests/LocalConversationTests.swift -o build/LocalConversationTests
build/LocalConversationTests
swiftc -parse-as-library -D CAMP_TESTING -swift-version 5 -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift Tests/CampWorkerTests.swift -o build/CampWorkerTests
build/CampWorkerTests
swiftc -parse-as-library -D CAMP_TESTING -swift-version 5 -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift Tests/SessionMentionsTests.swift -o build/SessionMentionsTests
build/SessionMentionsTests
