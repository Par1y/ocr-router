// OCR System JavaScript

// Utility functions
const OCRSystem = {
    // API base URL
    baseURL: '/api',

    // Make API request
    async request(endpoint, options = {}) {
        const url = `${this.baseURL}${endpoint}`;
        const response = await fetch(url, {
            headers: {
                'Content-Type': 'application/json',
                ...options.headers
            },
            ...options
        });

        if (!response.ok) {
            const error = await response.text();
            throw new Error(error || `HTTP ${response.status}`);
        }

        return response.json();
    },

    // Submit sync OCR task
    async recognizeSync(imagePath, provider = null, prompt = null) {
        const body = { image_path: imagePath };
        if (provider) body.provider = provider;
        if (prompt) body.prompt = prompt;

        return this.request('/ocr/sync', {
            method: 'POST',
            body: JSON.stringify(body)
        });
    },

    // Submit async OCR task
    async recognizeAsync(imagePath, provider = null, prompt = null) {
        const body = { image_path: imagePath };
        if (provider) body.provider = provider;
        if (prompt) body.prompt = prompt;

        return this.request('/ocr/async', {
            method: 'POST',
            body: JSON.stringify(body)
        });
    },

    // Get task status
    async getTask(taskId) {
        return this.request(`/tasks/${taskId}`);
    },

    // List tasks
    async listTasks(status = null) {
        const params = status ? `?status=${status}` : '';
        return this.request(`/tasks${params}`);
    },

    // Get providers status
    async getProviders() {
        return this.request('/providers');
    },

    // Health check
    async healthCheck() {
        return this.request('/health');
    },

    // Format date
    formatDate(dateString) {
        return new Date(dateString).toLocaleString();
    },

    // Format duration
    formatDuration(ms) {
        if (ms < 1000) return `${ms}ms`;
        if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
        return `${(ms / 60000).toFixed(1)}m`;
    },

    // Copy text to clipboard
    async copyToClipboard(text) {
        try {
            await navigator.clipboard.writeText(text);
            return true;
        } catch (err) {
            // Fallback for older browsers
            const textarea = document.createElement('textarea');
            textarea.value = text;
            document.body.appendChild(textarea);
            textarea.select();
            document.execCommand('copy');
            document.body.removeChild(textarea);
            return true;
        }
    },

    // Show notification
    showNotification(message, type = 'info') {
        const notification = document.createElement('div');
        notification.className = `notification notification-${type}`;
        notification.textContent = message;
        
        document.body.appendChild(notification);
        
        setTimeout(() => {
            notification.remove();
        }, 3000);
    }
};

// Export for use in templates
window.OCRSystem = OCRSystem;
