package worker

import (
	"errors"
	"time"

	"go.temporal.io/sdk/workflow"
)

type TamagotchiState struct {
	Name         string         `json:"name"`
	Hatched      time.Time      `json:"hatched"`
	Hunger       int            `json:"hunger"`
	Fun          int            `json:"fun"`
	Clean        int            `json:"clean"`
	Energy       int            `json:"energy"`
	Sleeping     bool           `json:"sleeping"`
	Deceased     *time.Time     `json:"deceased,omitempty"`
	CauseOfDeath *CauseOfDeath  `json:"causeOfDeath,omitempty"`
	Epitaph      string         `json:"epitaph,omitempty"`
	FoodStats    map[string]int `json:"foodStats"`
	PlayStats    map[string]int `json:"playStats"`
}

type TamagotchiRequest struct {
	Name  string           `json:"name"`
	State *TamagotchiState `json:"state"`
}

type CauseOfDeath string

const (
	CauseOfDeathOldAge     CauseOfDeath = "old age"
	CauseOfDeathStarvation CauseOfDeath = "starvation"
	CauseOfDeathExhaustion CauseOfDeath = "exhaustion"
	CauseOfDeathBoredom    CauseOfDeath = "boredom"
	CauseOfDeathDirtiness  CauseOfDeath = "dirtiness"
	CauseOfDeathFatigue    CauseOfDeath = "fatigue"
	CauseOfDeathIllness    CauseOfDeath = "illness"
)

func (c CauseOfDeath) Error() string {
	return string(c)
}

type FeedRequest struct {
	Food string `json:"food"`
}

type PlayRequest struct {
	Activity string `json:"activity"`
}

type SleepRequest struct{}

type BatheRequest struct{}

func Tamagotchi(ctx workflow.Context, req *TamagotchiRequest) (*TamagotchiState, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}

	if req.State == nil {
		req.State = &TamagotchiState{
			Name:      req.Name,
			Hatched:   workflow.Now(ctx),
			Hunger:    75,
			Fun:       50,
			Clean:     90,
			Energy:    100,
			Sleeping:  false,
			FoodStats: map[string]int{},
			PlayStats: map[string]int{},
		}
		req.State.log(ctx, "Tamagotchi workflow started")
	} else {
		if req.State.FoodStats == nil {
			req.State.FoodStats = map[string]int{}
		}
		if req.State.PlayStats == nil {
			req.State.PlayStats = map[string]int{}
		}
		req.State.log(ctx, "Tamagotchi workflow continued as new")
	}

	ch := workflow.NewNamedChannel(ctx, "update")

	req.State.registerQueryHandlers(ctx)
	req.State.registerUpdateHandlers(ctx, ch)

	// Run a background task to update the tamagotchi's stats as time passes
	workflow.GoNamed(ctx, "tick", func(ctx workflow.Context) {
		for {
			// Normally it would make sense to use workflow.Sleep here, but it's nicer to reserve Sleep events
			// in the workflow history for when the tamagotchi is actually "sleeping".
			workflow.NewTimer(ctx, 5*time.Second).Get(ctx, nil)

			// If sleeping, add to energy until full.
			if req.State.Sleeping {
				req.State.Energy = clampPercent(req.State.Energy + 25)
				if req.State.Energy >= 100 {
					req.State.Sleeping = false
				}
			} else {
				req.State.applyEffect(statsEffect{
					hunger: -3,
					energy: -3,
					fun:    -3,
					clean:  -3,
				})
			}

			// Wake up the game loop to check if the tamagotchi is dead
			ch.Send(ctx, nil)
		}
	})

	// Main game loop: block until the tamagotchi has a cause of death or it's time to continue as new.
	for {
		// Wait for an update
		ch.Receive(ctx, nil)
		if err := checkup(ctx, req); err != nil {
			if cause, ok := err.(CauseOfDeath); ok {
				timeOfDeath := workflow.Now(ctx)
				req.State.Deceased = &timeOfDeath
				req.State.CauseOfDeath = &cause
				req.State.Epitaph = generateEpitaph(req.State)
				req.State.log(ctx, "Tamagotchi has died", "cause", cause)
				return req.State, nil
			}
			return nil, err
		}

		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			return nil, workflow.NewContinueAsNewError(ctx, Tamagotchi, req)
		}
	}
}

func checkup(ctx workflow.Context, req *TamagotchiRequest) error {
	// Automatically put the tamagotchi to sleep if it's out of energy.
	if req.State.Energy == 0 {
		req.State.Sleeping = true
	}

	if req.State.Fun == 0 {
		return CauseOfDeathBoredom
	}

	if req.State.Clean == 0 {
		return CauseOfDeathDirtiness
	}

	if req.State.Hunger == 0 {
		return CauseOfDeathStarvation
	}

	// TODO(jlegrone): Maybe lift the maximum life expectancy :)
	if workflow.Now(ctx).After(req.State.Hatched.Add(24 * time.Hour)) {
		req.State.log(ctx, "Tamagotchi has lived for 24 hours")
		return CauseOfDeathOldAge
	}

	return nil
}

// statsEffect is a struct that represents the effect of an action on the tamagotchi's stats.
// Every stat change may be positive, negative, or neutral.
type statsEffect struct {
	hunger int
	energy int
	fun    int
	clean  int
}

func getFoodEffect(food string) (statsEffect, bool) {
	switch food {
	case "apple":
		return statsEffect{hunger: 10}, true
	case "banana":
		return statsEffect{hunger: 10}, true
	case "carrot":
		return statsEffect{hunger: 5}, true
	case "peanut":
		return statsEffect{hunger: 1}, true
	case "pizza":
		return statsEffect{hunger: 25}, true
	case "ice cream":
		return statsEffect{
			hunger: 15,
			fun:    10,
		}, true
	}
	return statsEffect{}, false
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func (s *TamagotchiState) applyEffect(effect statsEffect) {
	s.Hunger = clampPercent(s.Hunger + effect.hunger)
	s.Fun = clampPercent(s.Fun + effect.fun)
	s.Clean = clampPercent(s.Clean + effect.clean)
	s.Energy = clampPercent(s.Energy + effect.energy)
}

func (s *TamagotchiState) handleFeed(ctx workflow.Context, ch workflow.SendChannel) {
	workflow.SetUpdateHandlerWithOptions(ctx, "feed", func(ctx workflow.Context, req *FeedRequest) (*TamagotchiState, error) {
		s.log(ctx, "Feeding tamagotchi")
		effect, _ := getFoodEffect(req.Food)
		s.applyEffect(effect)
		s.FoodStats[req.Food]++
		ch.Send(ctx, nil)
		return s, nil
	}, workflow.UpdateHandlerOptions{
		Validator: func(ctx workflow.Context, req *FeedRequest) error {
			if s.Sleeping {
				return errors.New("your tamagotchi is sleeping right now")
			}
			effect, ok := getFoodEffect(req.Food)
			if !ok {
				return errors.New("your tamagotchi doesn't like that food")
			}
			if s.Hunger+effect.hunger > 100 {
				return errors.New("your tamagotchi is not hungry enough for that")
			}
			return nil
		},
	})
}

func (s *TamagotchiState) handleSleep(ctx workflow.Context, ch workflow.SendChannel) {
	workflow.SetUpdateHandlerWithOptions(ctx, "sleep", func(ctx workflow.Context, req *SleepRequest) error {
		s.log(ctx, "Putting tamagotchi to sleep")
		s.Sleeping = true
		ch.Send(ctx, nil)
		return nil
	}, workflow.UpdateHandlerOptions{
		Validator: func(ctx workflow.Context, req *SleepRequest) error {
			if s.Sleeping {
				return errors.New("your tamagotchi is already sleeping")
			}
			if s.Energy > 80 {
				return errors.New("your tamagotchi is not tired enough to sleep")
			}
			return nil
		},
	})
}

func getPlayEffect(activity string) (statsEffect, bool) {
	switch activity {
	case "walk":
		return statsEffect{
			fun:    10,
			hunger: -1,
			energy: -5,
			clean:  -5,
		}, true
	case "fetch":
		return statsEffect{
			fun:    30,
			hunger: -5,
			energy: -15,
			clean:  -10,
		}, true
	case "frisbee":
		return statsEffect{
			fun:    35,
			hunger: -8,
			energy: -20,
			clean:  -15,
		}, true
	case "chachacha":
		return statsEffect{
			fun:    40,
			hunger: -10,
			energy: -25,
			clean:  -1,
		}, true
	}
	return statsEffect{}, false
}

func (s *TamagotchiState) handlePlay(ctx workflow.Context, ch workflow.SendChannel) {
	workflow.SetUpdateHandlerWithOptions(ctx, "play", func(ctx workflow.Context, req *PlayRequest) error {
		s.log(ctx, "Playing with tamagotchi")
		effect, _ := getPlayEffect(req.Activity)
		s.applyEffect(effect)
		s.PlayStats[req.Activity]++
		ch.Send(ctx, nil)
		return nil
	}, workflow.UpdateHandlerOptions{
		Validator: func(ctx workflow.Context, req *PlayRequest) error {
			effect, ok := getPlayEffect(req.Activity)
			if !ok {
				return errors.New("your tamagotchi doesn't like that activity")
			}
			if s.Sleeping {
				return errors.New("your tamagotchi is sleeping right now")
			}
			if s.Energy+effect.energy < 1 {
				return errors.New("your tamagotchi is too tired for that")
			}
			if s.Hunger < 25 {
				return errors.New("your tamagotchi is too hungry to play")
			}
			return nil
		},
	})
}

func (s *TamagotchiState) handleBathe(ctx workflow.Context, ch workflow.SendChannel) {
	workflow.SetUpdateHandlerWithOptions(ctx, "bathe", func(ctx workflow.Context, req *BatheRequest) error {
		s.log(ctx, "Giving tamagotchi a bath")
		s.applyEffect(statsEffect{
			clean: 100,
			fun:   -10,
		})
		ch.Send(ctx, nil)
		return nil
	}, workflow.UpdateHandlerOptions{
		Validator: func(ctx workflow.Context, req *BatheRequest) error {
			if s.Sleeping {
				return errors.New("your tamagotchi is sleeping right now")
			}
			if s.Clean >= 90 {
				return errors.New("your tamagotchi is already clean")
			}
			return nil
		},
	})
}

type ChatRequest struct {
	Message string `json:"message"`
}

type ChatResponse struct {
	Response string `json:"response"`
}

func (s *TamagotchiState) handleChat(ctx workflow.Context, chatClient *TamaChat) {
	workflow.SetUpdateHandlerWithOptions(ctx, "chat", func(ctx workflow.Context, req *ChatRequest) (*ChatResponse, error) {
		s.log(ctx, "Chatting with tamagotchi")
		resp, err := chatClient.ExecuteActivityTamaChat(ctx, &TamaChatRequest{
			State:   s,
			Message: req.Message,
		})
		if err != nil {
			return nil, err
		}
		return &ChatResponse{
			Response: resp.Response,
		}, nil
	}, workflow.UpdateHandlerOptions{
		Validator: func(ctx workflow.Context, req *ChatRequest) error {
			if s.Sleeping {
				return errors.New("your tamagotchi is sleeping right now")
			}
			return nil
		},
	})
}

func (s *TamagotchiState) registerQueryHandlers(ctx workflow.Context) {
	if err := workflow.SetQueryHandlerWithOptions(ctx, "stats", func() (*TamagotchiState, error) {
		return s, nil
	}, workflow.QueryHandlerOptions{
		Description: "Get your Tamagotchi's current stats",
	}); err != nil {
		panic(err)
	}
}

func (s *TamagotchiState) registerUpdateHandlers(ctx workflow.Context, ch workflow.SendChannel) {
	s.handleFeed(ctx, ch)
	s.handleSleep(ctx, ch)
	s.handlePlay(ctx, ch)
	s.handleBathe(ctx, ch)
	s.handleChat(ctx, &TamaChat{})
}

func (s *TamagotchiState) log(ctx workflow.Context, msg string, keyvals ...any) {
	workflow.GetLogger(ctx).Info(msg, keyvals...)
}

func generateEpitaph(s *TamagotchiState) string {
	// Find favorite food
	favoriteFood := "nothing"
	mostEatenCount := 0
	for food, count := range s.FoodStats {
		if count > mostEatenCount {
			favoriteFood = food
			mostEatenCount = count
		}
	}

	// Find favorite activity
	favoriteActivity := ""
	mostPlayedCount := 0
	for activity, count := range s.PlayStats {
		if count > mostPlayedCount {
			favoriteActivity = activity
			mostPlayedCount = count
		}
	}

	epitaph := "Here lies " + s.Name + ", "

	if mostEatenCount > 0 {
		epitaph += "who loved eating " + favoriteFood
	}
	if mostPlayedCount > 0 {
		if mostEatenCount > 0 {
			epitaph += " and " + favoriteActivity
		} else {
			epitaph += "who loved " + favoriteActivity
		}
	}
	if len(s.FoodStats) <= 1 && len(s.PlayStats) <= 1 {
		epitaph += "who lived a simple life"
	}

	return epitaph
}
