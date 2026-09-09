package githubforge

var githubReactionEmojiByContent = map[string]string{
	"+1":       "👍",
	"-1":       "👎",
	"laugh":    "😄",
	"confused": "😕",
	"heart":    "❤️",
	"hooray":   "🎉",
	"rocket":   "🚀",
	"eyes":     "👀",
}

var githubReactionContentByEmoji = func() map[string]string {
	result := make(map[string]string, len(githubReactionEmojiByContent))
	for content, emoji := range githubReactionEmojiByContent {
		result[emoji] = content
	}
	return result
}()

// ReactionEmoji returns the NIP-25 reaction content for a GitHub reaction.
func ReactionEmoji(content string) (string, bool) {
	emoji, ok := githubReactionEmojiByContent[content]
	return emoji, ok
}

// ReactionContent returns the GitHub API value for a supported NIP-25 emoji.
func ReactionContent(emoji string) (string, bool) {
	content, ok := githubReactionContentByEmoji[emoji]
	return content, ok
}

func isGitHubReactionContent(content string) bool {
	_, ok := githubReactionEmojiByContent[content]
	return ok
}
