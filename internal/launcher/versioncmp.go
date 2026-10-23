package launcher

import "strings"

// normalizeVersion trims surrounding whitespace and a single leading "v"/"V".
func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 0 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	return version
}

type parsedVersion struct {
	numbers []uint64
	label   string
}

func parse(version string) parsedVersion {
	version = normalizeVersion(version)
	numbers := make([]uint64, 0, 4)
	position := 0
	for position < len(version) {
		character := version[position]
		if character >= '0' && character <= '9' {
			value := uint64(0)
			for position < len(version) && version[position] >= '0' && version[position] <= '9' {
				digit := uint64(version[position] - '0')
				if value > (^uint64(0)-digit)/10 {
					value = ^uint64(0)
				} else {
					value = value*10 + digit
				}
				position++
			}
			numbers = append(numbers, value)
			continue
		}
		switch character {
		case '.', '-', '_', '+', ' ', '\t':
			position++
		default:
			return parsedVersion{numbers: numbers, label: strings.ToLower(version[position:])}
		}
	}
	return parsedVersion{numbers: numbers}
}

// compareVersions returns -1 when a is older than b, 0 when they are
// equivalent, and 1 when a is newer.
func compareVersions(a, b string) int {
	first := parse(a)
	second := parse(b)

	count := len(first.numbers)
	if len(second.numbers) > count {
		count = len(second.numbers)
	}
	for i := 0; i < count; i++ {
		var firstNumber, secondNumber uint64
		if i < len(first.numbers) {
			firstNumber = first.numbers[i]
		}
		if i < len(second.numbers) {
			secondNumber = second.numbers[i]
		}
		switch {
		case firstNumber < secondNumber:
			return -1
		case firstNumber > secondNumber:
			return 1
		}
	}

	switch {
	case first.label < second.label:
		return -1
	case first.label > second.label:
		return 1
	default:
		return 0
	}
}
