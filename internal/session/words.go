package session

// Curated word lists for human-readable session IDs. Keeping the lists
// embedded avoids a dependency on a system dictionary and keeps IDs
// predictable across hosts. Each list must remain lowercase, hyphen-free,
// and free of duplicates; a test enforces that.
var (
	adjectives = []string{
		"amber", "ancient", "bold", "brave", "bright", "brisk", "calm",
		"careful", "cheerful", "clever", "coastal", "cosmic", "crisp",
		"curious", "dapper", "deep", "eager", "early", "earthy", "easy",
		"fair", "fast", "fierce", "fleet", "gentle", "glad", "golden",
		"grand", "green", "happy", "hardy", "honest", "humble", "icy",
		"jolly", "keen", "kind", "lively", "lucky", "mellow", "merry",
		"mighty", "mild", "nimble", "noble", "patient", "plain", "polite",
		"proud", "quick", "quiet", "rapid", "ready", "royal", "sharp",
		"silent", "silver", "simple", "steady", "strong", "sunny", "swift",
		"tidy", "vivid", "warm", "wild", "wise", "witty", "young",
	}

	nouns = []string{
		"acorn", "badger", "beacon", "birch", "breeze", "brook", "canyon",
		"cedar", "cloud", "comet", "creek", "dawn", "deer", "delta", "dog",
		"dove", "eagle", "ember", "falcon", "fern", "field", "finch",
		"fire", "forest", "fox", "frost", "garden", "glade", "grove",
		"harbor", "hawk", "heron", "hill", "hollow", "island", "ivy",
		"lake", "leaf", "meadow", "mist", "moon", "moss", "mountain",
		"oak", "ocean", "otter", "owl", "path", "pine", "pond", "rain",
		"raven", "reef", "ridge", "river", "rock", "shore", "sky", "snow",
		"spark", "star", "stone", "storm", "stream", "sun", "tide",
		"trail", "valley", "wave", "willow", "wind", "wolf", "wood",
	}

	verbs = []string{
		"walks", "runs", "sings", "shines", "listens", "watches", "finds",
		"builds", "carries", "climbs", "dances", "dreams", "drifts",
		"flows", "flies", "gathers", "glides", "grows", "hops", "hums",
		"jumps", "keeps", "leads", "learns", "looks", "marches", "meets",
		"moves", "opens", "paints", "plays", "reads", "rests", "rises",
		"roams", "sails", "seeks", "shapes", "shares", "sits", "sleeps",
		"smiles", "speaks", "stands", "stays", "steps", "swims",
		"teaches", "tells", "thinks", "travels", "turns", "waits",
		"wakes", "wanders", "waves", "whispers", "wins", "works",
		"writes", "yields",
	}

	adverbs = []string{
		"boldly", "bravely", "brightly", "briskly", "calmly", "carefully",
		"cheerfully", "cleverly", "eagerly", "easily", "fairly", "gently",
		"gladly", "happily", "kindly", "lightly", "loudly", "merrily",
		"mildly", "neatly", "nimbly", "patiently", "plainly", "politely",
		"proudly", "quickly", "quietly", "rapidly", "readily", "roughly",
		"sharply", "silently", "simply", "smoothly", "softly", "steadily",
		"strongly", "surely", "sweetly", "swiftly", "tenderly",
		"thoughtfully", "tightly", "warmly", "wildly", "wisely",
		"wittily",
	}
)
