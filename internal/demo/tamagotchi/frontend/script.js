window.addEventListener('load', () => {    
    if (window.location.hash) {
        showTamagotchi(null, getPetId());
    }
});

document.getElementById('egg-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const name = e.target.name.value;
    
    try {
        const response = await fetch('/api/hatch?name=' + name, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            // body: JSON.stringify({ name }),
        });

        if (response.ok) {
            const data = await response.json();

            // Set the id of the pet in the URL hash
            window.location.hash = data.id;

            showTamagotchi(name, data.id);
        }
    } catch (error) {
        console.error('Error:', error);
    }
});

async function updateStats(id) {
    try {
        const response = await fetch('/api/' + id + '/stats');
        const stats = await response.json();

        // API response looks like this:
        // {"name":"Larry","hatched":"2025-02-09T02:25:08.612462349Z","hunger":40,"fun":60,"clean":60,"energy":20,"sleeping":false}

        let lastDay = new Date();
        if (stats.deceased) {
            lastDay = new Date(stats.deceased);
        }
        const ageInMs = lastDay - new Date(stats.hatched);
        const ageInMinutes = Math.floor(ageInMs / (1000 * 60));
        const ageText = ageInMinutes < 60 
            ? `${ageInMinutes} minutes` 
            : `${Math.floor(ageInMinutes / 60)} hours`;

        document.getElementById('pet-name').textContent = stats.name;
        document.getElementById('happiness').textContent = stats.fun;
        document.getElementById('hunger').textContent = stats.hunger;
        document.getElementById('clean').textContent = stats.clean;
        document.getElementById('age').textContent = ageText;
        document.getElementById('energy').textContent = stats.energy;

        // Update image based on status
        updateImage(stats);

        // Update stat bars
        updateStat('happiness', stats.fun);
        updateStat('hunger', stats.hunger);
        updateStat('clean', stats.clean);
        updateStat('energy', stats.energy);
    } catch (error) {
        console.error('Error updating stats:', error);
    }
}

function updateImage(stats) {
    const img = document.getElementById('tamagotchi-img');
    const tamagotchiDisplay = document.getElementById('tamagotchi-display');
    const memorial = document.getElementById('memorial');
    
    if (stats.deceased) {
        img.src = '/images/rip.jpeg';
        tamagotchiDisplay.classList.add('deceased');

        // Show memorial
        memorial.style.display = 'block';

        // Set death cause
        const deathCause = document.getElementById('death-cause');
        deathCause.textContent = `Cause of death: ${stats.causeOfDeath || 'Unknown'}`;

        // Create epitaph based on favorite activities
        const epitaph = document.getElementById('epitaph');
        epitaph.textContent = stats.epitaph;

        // Set life dates separately
        const hatched = new Date(stats.hatched);
        const deceased = new Date(stats.deceased);
        const lifeDates = document.getElementById('life-dates');
        lifeDates.textContent = `${hatched.toLocaleDateString()} - ${deceased.toLocaleDateString()}`;
    } else {
        memorial.style.display = 'none';
        tamagotchiDisplay.classList.remove('deceased');
        if (stats.sleeping) {
            img.src = '/images/sleeping.jpeg';
        } else if (stats.hunger <= 40) {
            img.src = '/images/hungry.jpeg';
        } else if (stats.fun < 40) {
            img.src = '/images/bored.jpeg';
        } else if (stats.clean < 40) {
            // TODO: add dirty image
            img.src = '/images/hungry.jpeg';
        } else if (stats.energy < 40) {
            img.src = '/images/tired.jpeg';
        } else if (stats.fun + stats.hunger + stats.energy + stats.clean > 360) {
            img.src = '/images/happy.jpeg';
        } else {
            img.src = '/images/baby.jpeg';
        }
    }
}

function getPetId() {
    return window.location.hash.slice(1);
}

async function tryAction(endpoint, formData = null, id) {
    try {
        const options = {
            method: 'POST',
        };
        if (formData) {
            options.body = formData;
        }

        const response = await fetch(endpoint, options);

        if (!response.ok) {
            showError(response.statusText);
            return;
        }

        // Check if response contains JSON error message
        const data = await response.json();
        if (data.error) {
            showError(data.error);
            return;
        }

        // Update stats and return success
        updateStats(id);
        return true;
    } catch (error) {
        console.error(`Error during ${endpoint}:`, error);
        showError(`Failed to ${endpoint}`);
        return;
    }
}

async function feedPet() {
    const id = getPetId();
    const dialog = document.getElementById('food-dialog');
    dialog.showModal();
    dialog.addEventListener('close', async () => {
        if (dialog.returnValue && dialog.returnValue !== '') {
            const formData = new FormData();
            formData.append('food', dialog.returnValue);
            await tryAction('/api/' + id + '/feed', formData, id);
        }
    }, { once: true });
}

async function chatWithPet() {
    const id = getPetId();
    const dialog = document.getElementById('chat-dialog');
    const form = dialog.querySelector('form');
    
    dialog.showModal();
    
    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        const message = form.message.value;
        const formData = new FormData();
        formData.append('message', message);
        await tryAction('/api/' + id + '/chat', formData, id);
        dialog.close();
        form.reset();
    }, { once: true });
}

async function putToSleep() {
    const id = getPetId();
    await tryAction('/api/' + id + '/sleep', null, id);
}

async function bathePet() {
    const id = getPetId();
    await tryAction('/api/' + id + '/bathe', null, id);
}

async function playWithPet() {
    const id = getPetId();
    const dialog = document.getElementById('play-dialog');
    
    dialog.showModal();
    dialog.addEventListener('close', async () => {
        if (dialog.returnValue && dialog.returnValue !== '') {
            const formData = new FormData();
            formData.append('activity', dialog.returnValue);
            await tryAction('/api/' + id + '/play', formData, id);
        }
    }, { once: true });
}

function showTamagotchi(name, id) {
    document.getElementById('egg-form').style.display = 'none';
    // Start periodic updates
    updateStats(id);
    document.getElementById('tamagotchi-display').style.display = 'block';
    setInterval(updateStats, 3000, id); // Update every 3 seconds
}

function updateStat(id, value) {
    const element = document.getElementById(id);
    element.style.width = value + '%';
    element.textContent = value;
}

function showError(message) {
    const errorDiv = document.getElementById('error-message');
    errorDiv.textContent = message;
    
    // Clear the message after 3 seconds
    setTimeout(() => {
        errorDiv.textContent = '';
    }, 3000);
}
