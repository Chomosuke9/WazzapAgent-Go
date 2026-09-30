package discord

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/action"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
)

// Discord's component limits.
const (
	maxActionRows      = 5
	maxButtonsPerRow   = 5
	maxCustomIDBytes   = 100
	maxButtonLabelRune = 80
	maxOptionTextRunes = 100
)

// quizPrefix marks a quiz button. Its tap arrives as the choice's label, so
// the conversation reads as if the user typed their answer.
const quizPrefix = "quiz:"

// menuPrefix marks a select menu; the picked option's value is what a tap
// sends back.
const menuPrefix = "menu:"

// quizComponents is one button per choice, under the reply that asks.
func quizComponents(choices []string) []discordgo.MessageComponent {
	var rows []discordgo.MessageComponent
	var row []discordgo.MessageComponent
	for index, choice := range choices {
		row = append(row, discordgo.Button{
			Label: truncateRunes(choice, maxButtonLabelRune), Style: discordgo.SecondaryButton,
			CustomID: quizPrefix + strconv.Itoa(index+1),
		})
		if len(row) == maxButtonsPerRow {
			rows = append(rows, discordgo.ActionsRow{Components: row})
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, discordgo.ActionsRow{Components: row})
	}
	return rows
}

// buttonComponents lays out a command's buttons, five to a row, then one row
// per menu. A layout Discord cannot show fails as unsupported, so the caller
// sends the text form instead.
func buttonComponents(request action.SendButtonsRequest) ([]discordgo.MessageComponent, error) {
	count := len(request.Buttons) + len(request.Menus)
	if strings.TrimSpace(request.Text) == "" || count == 0 || count > action.MaxButtons {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "build Discord buttons", errors.New("text and 1 to 10 buttons and menus are required"))
	}
	var rows []discordgo.MessageComponent
	var row []discordgo.MessageComponent
	for _, button := range request.Buttons {
		if strings.TrimSpace(button.ID) == "" || strings.TrimSpace(button.Label) == "" {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "build Discord buttons", errors.New("every button needs an ID and a label"))
		}
		if len(button.ID) > maxCustomIDBytes {
			return nil, agent.NewError(agent.ErrorUnsupported, "build Discord buttons", errors.New("a button's command is longer than Discord allows"))
		}
		row = append(row, discordgo.Button{Label: truncateRunes(button.Label, maxButtonLabelRune), Style: discordgo.SecondaryButton, CustomID: button.ID})
		if len(row) == maxButtonsPerRow {
			rows = append(rows, discordgo.ActionsRow{Components: row})
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, discordgo.ActionsRow{Components: row})
	}
	for index, menu := range request.Menus {
		if strings.TrimSpace(menu.Title) == "" || len(menu.Rows) == 0 || len(menu.Rows) > action.MaxMenuRows {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "build Discord menu", errors.New("every menu needs a title and 1 to 10 rows"))
		}
		options := make([]discordgo.SelectMenuOption, 0, len(menu.Rows))
		seen := make(map[string]struct{}, len(menu.Rows))
		for _, option := range menu.Rows {
			if strings.TrimSpace(option.ID) == "" || strings.TrimSpace(option.Title) == "" {
				return nil, agent.NewError(agent.ErrorInvalidArgument, "build Discord menu", errors.New("every menu row needs an ID and a title"))
			}
			if len(option.ID) > maxCustomIDBytes {
				return nil, agent.NewError(agent.ErrorUnsupported, "build Discord menu", errors.New("a menu row's command is longer than Discord allows"))
			}
			if _, duplicate := seen[option.ID]; duplicate {
				return nil, agent.NewError(agent.ErrorUnsupported, "build Discord menu", errors.New("Discord menus need distinct row values"))
			}
			seen[option.ID] = struct{}{}
			options = append(options, discordgo.SelectMenuOption{
				Label: truncateRunes(option.Title, maxOptionTextRunes), Value: option.ID,
				Description: truncateRunes(option.Description, maxOptionTextRunes),
			})
		}
		rows = append(rows, discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{
			MenuType: discordgo.StringSelectMenu, CustomID: menuPrefix + strconv.Itoa(index+1),
			Placeholder: truncateRunes(menu.Title, maxOptionTextRunes), Options: options,
		}}})
	}
	if len(rows) > maxActionRows {
		return nil, agent.NewError(agent.ErrorUnsupported, "build Discord buttons", fmt.Errorf("the buttons and menus need more than %d rows", maxActionRows))
	}
	return rows, nil
}

// tappedText is what a tap on one of the bot's components says, as the
// user's message: a command button or menu row sends its command, a quiz
// button the choice it shows, and anything else its label.
func tappedText(data discordgo.MessageComponentInteractionData, message *discordgo.Message) string {
	switch data.ComponentType {
	case discordgo.ButtonComponent:
		if strings.HasPrefix(data.CustomID, "/") {
			return data.CustomID
		}
		if label := buttonLabel(message, data.CustomID); label != "" {
			return label
		}
		if strings.HasPrefix(data.CustomID, quizPrefix) {
			return ""
		}
		return data.CustomID
	case discordgo.SelectMenuComponent:
		if len(data.Values) == 0 {
			return ""
		}
		value := data.Values[0]
		if strings.HasPrefix(value, "/") {
			return value
		}
		if label := optionLabel(message, data.CustomID, value); label != "" {
			return label
		}
		return value
	}
	return ""
}

func buttonLabel(message *discordgo.Message, customID string) string {
	if message == nil {
		return ""
	}
	for _, row := range message.Components {
		actions, ok := row.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, component := range actions.Components {
			if button, ok := component.(*discordgo.Button); ok && button.CustomID == customID {
				return strings.TrimSpace(button.Label)
			}
		}
	}
	return ""
}

func optionLabel(message *discordgo.Message, customID, value string) string {
	if message == nil {
		return ""
	}
	for _, row := range message.Components {
		actions, ok := row.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, component := range actions.Components {
			menu, ok := component.(*discordgo.SelectMenu)
			if !ok || menu.CustomID != customID {
				continue
			}
			for _, option := range menu.Options {
				if option.Value == value {
					return strings.TrimSpace(option.Label)
				}
			}
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
